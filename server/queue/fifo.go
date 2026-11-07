// Copyright 2022 Woodpecker Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package queue

import (
	"cmp"
	"container/list"
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"go.woodpecker-ci.org/woodpecker/v3/server/model"
	"go.woodpecker-ci.org/woodpecker/v3/shared/constant"
)

type entry struct {
	item     *model.Task
	done     chan bool
	error    error
	deadline time.Time
}

type worker struct {
	agentID int64
	filter  func(*model.Task) (bool, int)
	channel chan *model.Task
	stop    context.CancelCauseFunc
}

// groupHolds are the pipelines holding a concurrency group without a limit
// (see model.Task.ExclusiveGroup): by pipeline ID, the tasks of the pipeline in
// the group that are not done yet. A task the queue does not have, one running
// on an agent since before a server restart, maps to the deadline by which its
// agent has to extend it; the others map to the zero time.
type groupHolds map[int64]map[string]time.Time

type fifo struct {
	sync.Mutex

	ctx           context.Context
	workers       map[*worker]struct{}
	running       map[string]*entry
	pending       *list.List
	waitingOnDeps *list.List
	extension     time.Duration
	paused        bool
	// holds are the concurrency groups without a limit that are in progress,
	// by group. Another pipeline's tasks in a group are not handed out while
	// it is held.
	holds map[string]groupHolds
	// lost are the held tasks the queue does not have that were released as
	// their agent did not extend them in time. They are not held again, so a
	// workflow left running in the store by a lost agent does not hold up every
	// new pipeline in its group.
	lost map[string]struct{}
}

// processTimeInterval is the time till the queue rearranges things,
// as the agent pull in 10 milliseconds we should also give them work asap.
const processTimeInterval = 100 * time.Millisecond

// NewMemoryQueue returns a new fifo queue.
func NewMemoryQueue(ctx context.Context) Queue {
	q := &fifo{
		ctx:           ctx,
		workers:       map[*worker]struct{}{},
		running:       map[string]*entry{},
		pending:       list.New(),
		waitingOnDeps: list.New(),
		extension:     constant.TaskTimeout,
		paused:        false,
		holds:         map[string]groupHolds{},
		lost:          map[string]struct{}{},
	}
	go q.process()
	return q
}

// PushAtOnce pushes multiple tasks to the tail of this queue. A task queued in
// place of an earlier pipeline (see model.Task.QueuedInPlaceOf) goes to that
// pipeline's place instead: before the first pending task that comes after it
// in pipeline order.
func (q *fifo) PushAtOnce(_ context.Context, tasks []*model.Task) error {
	q.Lock()
	for _, task := range tasks {
		if task.QueuedInPlaceOf == 0 {
			q.pending.PushBack(task)
			continue
		}
		q.insertPending(task)
	}
	q.Unlock()
	return nil
}

// insertPending inserts the task into pending before the first task that comes
// after it in pipeline order, or at the tail if there is none.
//
// Expects the queue to be locked by the caller.
func (q *fifo) insertPending(task *model.Task) {
	for element := q.pending.Front(); element != nil; element = element.Next() {
		pending, _ := element.Value.(*model.Task)
		if comparePipelineOrder(task, pending) < 0 {
			q.pending.InsertBefore(task, element)
			return
		}
	}
	q.pending.PushBack(task)
}

// Poll retrieves and removes a task head of this queue.
func (q *fifo) Poll(c context.Context, agentID int64, filter func(*model.Task) (bool, int)) (*model.Task, error) {
	q.Lock()
	ctx, stop := context.WithCancelCause(c)

	w := &worker{
		agentID: agentID,
		channel: make(chan *model.Task, 1),
		filter:  filter,
		stop:    stop,
	}
	q.workers[w] = struct{}{}
	q.Unlock()

	for {
		select {
		case <-ctx.Done():
			q.Lock()
			delete(q.workers, w)
			q.Unlock()
			return nil, ctx.Err()
		case t := <-w.channel:
			return t, nil
		}
	}
}

// Done signals the task is complete.
func (q *fifo) Done(_ context.Context, id string, exitStatus model.StatusValue) error {
	return q.finished([]string{id}, exitStatus, nil)
}

// Error signals the task is done with an error.
func (q *fifo) Error(_ context.Context, id string, err error) error {
	return q.finished([]string{id}, model.StatusFailure, err)
}

// ErrorAtOnce signals multiple tasks are done and complete with an error.
// If still pending they will just get removed from the queue.
func (q *fifo) ErrorAtOnce(_ context.Context, ids []string, err error) error {
	if errors.Is(err, ErrCancel) {
		return q.finished(ids, model.StatusKilled, err)
	}
	return q.finished(ids, model.StatusFailure, err)
}

// locks the queue itself!
func (q *fifo) finished(ids []string, exitStatus model.StatusValue, err error) error {
	q.Lock()
	defer q.Unlock()

	// it's an external error so we wrap it
	err = NewErrExternal(err)

	var errs []error
	// we first process the tasks itself
	for _, id := range ids {
		// a task running on an agent since before a restart is not in the
		// queue, but may hold its concurrency group
		wasHeld := q.releaseHeld(id)
		delete(q.lost, id)
		if taskEntry, ok := q.running[id]; ok {
			taskEntry.error = err
			close(taskEntry.done)
			delete(q.running, id)
		} else if removeErr := q.removeFromPendingAndWaiting(id); removeErr != nil && !wasHeld {
			errs = append(errs, removeErr)
		}
	}

	// next we aim for there dependencies
	// we do this because in our ids list there could be tasks and its dependencies
	// so not to mess things up
	for _, id := range ids {
		q.updateDepStatusInQueue(id, exitStatus)
	}

	return errors.Join(errs...)
}

// Wait waits until the item is done executing.
// Also signals via error ErrCancel if workflow got canceled.
func (q *fifo) Wait(ctx context.Context, taskID string) error {
	q.Lock()
	state := q.running[taskID]
	q.Unlock()
	if state != nil {
		select {
		case <-ctx.Done():
		case <-state.done:
			// check if we have a wrapped cancel error and unwrap it
			if errors.Is(state.error, ErrCancel) {
				return ErrCancel
			}
			// or return queue errors and no workflow errors
			if !errors.Is(state.error, new(ErrExternal)) {
				return state.error
			}
		}
	}
	return nil
}

// Extend extends the task execution deadline.
func (q *fifo) Extend(_ context.Context, agentID int64, taskID string) error {
	q.Lock()
	defer q.Unlock()

	state, ok := q.running[taskID]
	if ok {
		if state.item.AgentID != agentID {
			return ErrAgentMissMatch
		}

		state.deadline = time.Now().Add(q.extension)
		return nil
	}
	// a task running on an agent since before a restart, holding its group
	for _, holds := range q.holds {
		for _, tasks := range holds {
			if deadline, held := tasks[taskID]; held && !deadline.IsZero() {
				tasks[taskID] = time.Now().Add(q.extension)
				return nil
			}
		}
	}
	return ErrNotFound
}

// Info returns internal queue information.
func (q *fifo) Info(_ context.Context) InfoT {
	q.Lock()
	stats := InfoT{}
	stats.Stats.Workers = len(q.workers)
	stats.Stats.Pending = q.pending.Len()
	stats.Stats.WaitingOnDeps = q.waitingOnDeps.Len()
	stats.Stats.Running = len(q.running)

	for element := q.pending.Front(); element != nil; element = element.Next() {
		task, _ := element.Value.(*model.Task)
		stats.Pending = append(stats.Pending, task)
	}
	for element := q.waitingOnDeps.Front(); element != nil; element = element.Next() {
		task, _ := element.Value.(*model.Task)
		stats.WaitingOnDeps = append(stats.WaitingOnDeps, task)
	}
	for _, entry := range q.running {
		stats.Running = append(stats.Running, entry.item)
	}
	stats.Paused = q.paused

	q.Unlock()
	return stats
}

// Pause stops the queue from handing out new work items in Poll.
func (q *fifo) Pause() {
	q.Lock()
	q.paused = true
	q.Unlock()
}

// Resume starts the queue again.
func (q *fifo) Resume() {
	q.Lock()
	q.paused = false
	q.Unlock()
}

// KickAgentWorkers kicks all workers for a given agent.
func (q *fifo) KickAgentWorkers(agentID int64) {
	q.Lock()
	defer q.Unlock()

	for worker := range q.workers {
		if worker.agentID == agentID {
			worker.stop(ErrWorkerKicked)
			delete(q.workers, worker)
		}
	}
}

// helper function that loops through the queue and attempts to
// match the item to a single subscriber until context got cancel.
func (q *fifo) process() {
	for {
		select {
		case <-time.After(processTimeInterval):
		case <-q.ctx.Done():
			return
		}

		q.Lock()
		if q.paused {
			q.Unlock()
			continue
		}

		q.resubmitExpiredPipelines()
		q.releaseExpiredHolds()
		q.filterWaiting()
		for pending, worker := q.assignToWorker(); pending != nil && worker != nil; pending, worker = q.assignToWorker() {
			task, _ := pending.Value.(*model.Task)
			task.AgentID = worker.agentID
			delete(q.workers, worker)
			q.holdGroup(task)
			q.pending.Remove(pending)
			q.running[task.ID] = &entry{
				item:     task,
				done:     make(chan bool),
				deadline: time.Now().Add(q.extension),
			}
			worker.channel <- task
		}
		q.Unlock()
	}
}

func (q *fifo) filterWaiting() {
	// resubmits all waiting tasks to pending, deps may have cleared
	q.resubmitWaiting()

	// rebuild waitingDeps
	q.waitingOnDeps = list.New()
	var filtered []*list.Element
	for element := q.pending.Front(); element != nil; element = element.Next() {
		task, _ := element.Value.(*model.Task)
		if q.depsInQueue(task) {
			log.Debug().Msgf("queue: waiting due to unmet dependencies %v", task.ID)
			q.waitingOnDeps.PushBack(task)
			filtered = append(filtered, element)
		}
	}

	// filter waiting tasks
	for _, f := range filtered {
		q.pending.Remove(f)
	}
}

// resubmitWaiting moves every task waiting on dependencies back to pending at
// its pipeline's place: before the first pending task of a later pipeline,
// instead of at the back behind every pipeline created since. Otherwise a
// workflow whose dependencies clear (e.g. a deploy after its build) waits for
// all later pipelines' workflows. Tasks of the same pipeline keep the order
// they were queued in, behind the ones already pending.
//
// The waiting tasks are sorted first, so a single walk over pending places
// them all.
//
// Expects the queue to be locked by the caller.
func (q *fifo) resubmitWaiting() {
	if q.waitingOnDeps.Len() == 0 {
		return
	}

	waiting := make([]*model.Task, 0, q.waitingOnDeps.Len())
	for element := q.waitingOnDeps.Front(); element != nil; element = element.Next() {
		task, _ := element.Value.(*model.Task)
		waiting = append(waiting, task)
	}
	slices.SortStableFunc(waiting, comparePipelineOrder)

	// A later waiting task never goes before an earlier one's place, so the
	// walk carries on from where the previous task was inserted.
	next := q.pending.Front()
	for _, task := range waiting {
		for next != nil {
			pending, _ := next.Value.(*model.Task)
			if comparePipelineOrder(task, pending) < 0 {
				break
			}
			next = next.Next()
		}
		if next == nil {
			q.pending.PushBack(task)
		} else {
			q.pending.InsertBefore(task, next)
		}
	}
}

// comparePipelineOrder orders tasks by the creation of their pipeline: by the
// Created timestamp, with the pipeline ID as tiebreaker for pipelines created
// within the same second. A task queued in place of an earlier pipeline is
// ordered by that pipeline's ID, right after it. Tasks of the same pipeline
// compare equal.
func comparePipelineOrder(a, b *model.Task) int {
	return cmp.Or(
		cmp.Compare(a.Created, b.Created),
		cmp.Compare(queuePlace(a), queuePlace(b)),
		cmp.Compare(a.PipelineID, b.PipelineID),
	)
}

// queuePlace is the ID of the pipeline whose place in the queue the task has.
func queuePlace(task *model.Task) int64 {
	if task.QueuedInPlaceOf != 0 {
		return task.QueuedInPlaceOf
	}
	return task.PipelineID
}

func (q *fifo) assignToWorker() (*list.Element, *worker) {
	var bestWorker *worker
	var bestScore int

	for element := q.pending.Front(); element != nil; element = element.Next() {
		task, _ := element.Value.(*model.Task)
		log.Debug().Msgf("queue: trying to assign task: %v with deps %v", task.ID, task.Dependencies)

		// skip tasks that would exceed their workflow concurrency limit, they
		// stay pending and are retried on the next process tick.
		if !q.canRunConcurrent(task) {
			log.Debug().Msgf("queue: task %v deferred due to concurrency group %q", task.ID, task.ConcurrencyGroup)
			continue
		}

		// skip tasks in a concurrency group without a limit that another
		// pipeline holds, they stay pending until it is done.
		if !q.canRunInGroup(task) {
			log.Debug().Msgf("queue: task %v deferred, another pipeline holds concurrency group %q", task.ID, task.ConcurrencyGroup)
			continue
		}

		for worker := range q.workers {
			matched, score := worker.filter(task)
			if matched && score > bestScore {
				bestWorker = worker
				bestScore = score
			}
		}
		if bestWorker != nil {
			log.Debug().Msgf("queue: assigned task: %v with deps %v to worker with score %d", task.ID, task.Dependencies, bestScore)
			return element, bestWorker
		}
	}

	return nil, nil
}

// canRunConcurrent reports whether the given task may currently start without
// violating its workflow concurrency limit. Tasks without a limit always pass,
// keeping the default scheduling behavior unchanged.
//
// Slots within a concurrency group are granted in creation order (earliest
// pipeline first, by the task's Created timestamp, with the workflow name as a
// deterministic tiebreaker) rather than in the order tasks become ready. This
// guarantees that a later pipeline whose dependencies happen to finish faster
// cannot overtake an earlier one that is still waiting.
//
// The ordering reservation only applies across pipelines. Within a single
// pipeline, execution order is already defined by depends_on, and reserving a
// slot for an earlier workflow could deadlock when it depends on a later
// workflow that shares the same group (e.g. deploy.yaml depending on
// test.yaml). Because dependencies never cross pipelines, restricting the
// reservation to other pipelines keeps the ordering guarantee while making
// such deadlocks impossible.
//
// Expects the queue to be locked by the caller.
func (q *fifo) canRunConcurrent(task *model.Task) bool {
	if task.ConcurrencyLimit <= 0 || task.ConcurrencyGroup == "" {
		return true
	}

	group := task.ConcurrencyGroup

	// count tasks of the same group that already occupy a running slot.
	running := 0
	for _, e := range q.running {
		if e.item.ConcurrencyGroup == group {
			running++
		}
	}
	if running >= task.ConcurrencyLimit {
		return false
	}

	// count not-yet-running members of the group from earlier pipelines. They
	// have priority for the remaining slots, even if they are still waiting on
	// their dependencies, so cross-pipeline ordering is preserved.
	ahead := 0
	countAhead := func(other *model.Task) {
		if other.ConcurrencyGroup != group || other.ID == task.ID {
			return
		}
		// only reserve order across pipelines; see the function doc above.
		if other.PipelineID == task.PipelineID {
			return
		}
		if taskOrderLess(other, task) {
			ahead++
		}
	}
	for element := q.pending.Front(); element != nil; element = element.Next() {
		other, _ := element.Value.(*model.Task)
		countAhead(other)
	}
	for element := q.waitingOnDeps.Front(); element != nil; element = element.Next() {
		other, _ := element.Value.(*model.Task)
		countAhead(other)
	}

	return running+ahead < task.ConcurrencyLimit
}

// canRunInGroup reports whether the given task may be handed out without
// running alongside another pipeline's tasks in its concurrency group without
// a limit: a pipeline holds the group from when the first of its tasks in it is
// handed out until all of them are done, and only its own tasks in the group
// are handed out meanwhile. A pipeline never waits for itself, so its tasks in
// the group run in the order of their dependencies. Tasks in no such group
// always pass, and so do tasks that are only handed out to be marked skipped
// as their dependencies preclude running.
//
// Expects the queue to be locked by the caller.
func (q *fifo) canRunInGroup(task *model.Task) bool {
	group := task.ExclusiveGroup()
	if group == "" || !task.ShouldRun() {
		return true
	}
	for pipelineID := range q.holds[group] {
		if pipelineID != task.PipelineID {
			return false
		}
	}
	return true
}

// holdGroup makes the pipeline of a task that is handed out hold the task's
// concurrency group without a limit, with all its tasks in the group the queue
// has, until they are done. A task handed out to be marked skipped does not
// start the pipeline's unit in the group.
//
// Expects the queue to be locked by the caller.
func (q *fifo) holdGroup(task *model.Task) {
	group := task.ExclusiveGroup()
	if group == "" || !task.ShouldRun() {
		return
	}
	tasks := q.heldTasks(group, task.PipelineID)
	tasks[task.ID] = time.Time{}
	q.eachTask(func(other *model.Task) {
		if other.PipelineID == task.PipelineID && other.ExclusiveGroup() == group {
			tasks[other.ID] = time.Time{}
		}
	})
}

// heldTasks returns the tasks by which the pipeline holds the group, adding
// the hold if there is none.
//
// Expects the queue to be locked by the caller.
func (q *fifo) heldTasks(group string, pipelineID int64) map[string]time.Time {
	holds, ok := q.holds[group]
	if !ok {
		holds = groupHolds{}
		q.holds[group] = holds
	}
	tasks, ok := holds[pipelineID]
	if !ok {
		tasks = map[string]time.Time{}
		holds[pipelineID] = tasks
	}
	return tasks
}

// HoldGroup makes the pipeline hold the concurrency group without a limit until
// the given tasks of it are done; see Queue.HoldGroup.
func (q *fifo) HoldGroup(_ context.Context, group string, pipelineID int64, taskIDs []string) {
	if group == "" || len(taskIDs) == 0 {
		return
	}

	q.Lock()
	defer q.Unlock()

	deadline := time.Now().Add(q.extension)
	for _, id := range taskIDs {
		if q.holdsTask(id) {
			continue
		}
		if _, lost := q.lost[id]; lost {
			continue
		}
		if q.has(id) {
			q.heldTasks(group, pipelineID)[id] = time.Time{}
		} else {
			// running on an agent since before a restart: held while the agent
			// extends it, like a running task
			q.heldTasks(group, pipelineID)[id] = deadline
		}
	}
}

// holdsTask reports whether the task holds a concurrency group.
//
// Expects the queue to be locked by the caller.
func (q *fifo) holdsTask(taskID string) bool {
	for _, holds := range q.holds {
		for _, tasks := range holds {
			if _, held := tasks[taskID]; held {
				return true
			}
		}
	}
	return false
}

// releaseHeld removes the task from the group holds it is in, releasing the
// groups whose pipeline has no tasks left in them. It reports whether the task
// was held.
//
// Expects the queue to be locked by the caller.
func (q *fifo) releaseHeld(taskID string) bool {
	held := false
	for group, holds := range q.holds {
		for pipelineID, tasks := range holds {
			if _, ok := tasks[taskID]; !ok {
				continue
			}
			held = true
			delete(tasks, taskID)
			if len(tasks) == 0 {
				delete(holds, pipelineID)
			}
		}
		if len(holds) == 0 {
			delete(q.holds, group)
		}
	}
	return held
}

// releaseExpiredHolds releases the held tasks the queue does not have whose
// agent did not extend them in time, like resubmitExpiredPipelines does for
// running tasks. Otherwise an agent lost during a server restart would hold the
// group for good.
//
// Expects the queue to be locked by the caller.
func (q *fifo) releaseExpiredHolds() {
	now := time.Now()
	for group, holds := range q.holds {
		for pipelineID, tasks := range holds {
			for id, deadline := range tasks {
				if !deadline.IsZero() && now.After(deadline) {
					log.Info().Msgf("queue: releasing expired task %s from concurrency group %q", id, group)
					delete(tasks, id)
					q.lost[id] = struct{}{}
				}
			}
			if len(tasks) == 0 {
				delete(holds, pipelineID)
			}
		}
		if len(holds) == 0 {
			delete(q.holds, group)
		}
	}
}

// Supersede removes the given tasks from the queue if none of them was handed
// out yet; see Queue.Supersede.
func (q *fifo) Supersede(_ context.Context, ids []string) (bool, error) {
	q.Lock()
	defer q.Unlock()

	type queued struct {
		list    *list.List
		element *list.Element
	}
	found := make([]queued, 0, len(ids))
	for _, id := range ids {
		if _, running := q.running[id]; running {
			return false, nil
		}
		if q.holdsTask(id) {
			return false, nil
		}
		element, from := q.findQueued(id)
		if element == nil {
			return false, nil
		}
		found = append(found, queued{list: from, element: element})
	}

	for _, task := range found {
		task.list.Remove(task.element)
	}
	// their dependents in the queue see them skipped
	for _, id := range ids {
		q.updateDepStatusInQueue(id, model.StatusSkipped)
	}
	return true, nil
}

// findQueued returns the element of the task with the given ID in pending or
// waiting on dependencies, and the list it is in, or nil if it is in neither.
//
// Expects the queue to be locked by the caller.
func (q *fifo) findQueued(taskID string) (*list.Element, *list.List) {
	for _, tasks := range []*list.List{q.pending, q.waitingOnDeps} {
		for element := tasks.Front(); element != nil; element = element.Next() {
			if task, _ := element.Value.(*model.Task); task.ID == taskID {
				return element, tasks
			}
		}
	}
	return nil, nil
}

// has reports whether the queue has the task with the given ID: pending,
// waiting on dependencies or running.
//
// Expects the queue to be locked by the caller.
func (q *fifo) has(taskID string) bool {
	if _, ok := q.running[taskID]; ok {
		return true
	}
	element, _ := q.findQueued(taskID)
	return element != nil
}

// eachTask calls f for every task the queue has: pending, waiting on
// dependencies and running.
//
// Expects the queue to be locked by the caller.
func (q *fifo) eachTask(f func(*model.Task)) {
	for element := q.pending.Front(); element != nil; element = element.Next() {
		task, _ := element.Value.(*model.Task)
		f(task)
	}
	for element := q.waitingOnDeps.Front(); element != nil; element = element.Next() {
		task, _ := element.Value.(*model.Task)
		f(task)
	}
	for _, entry := range q.running {
		f(entry.item)
	}
}

// taskOrderLess reports whether task a was instantiated before task b. Ordering
// is by the Created timestamp (the pipeline creation time), with the workflow
// name as a deterministic tiebreaker for tasks created within the same second.
// The task ID is intentionally not used for ordering.
func taskOrderLess(a, b *model.Task) bool {
	if a.Created != b.Created {
		return a.Created < b.Created
	}
	return a.Name < b.Name
}

func (q *fifo) resubmitExpiredPipelines() {
	for taskID, taskState := range q.running {
		if time.Now().After(taskState.deadline) {
			log.Info().Msgf("queue: resubmitting expired task %s", taskID)
			taskState.error = ErrTaskExpired
			q.pending.PushFront(taskState.item)
			delete(q.running, taskID)
			close(taskState.done)
		}
	}
}

func (q *fifo) depsInQueue(task *model.Task) bool {
	for element := q.pending.Front(); element != nil; element = element.Next() {
		possibleDep, ok := element.Value.(*model.Task)
		log.Debug().Msgf("queue: pending right now: %v", possibleDep.ID)
		for _, dep := range task.Dependencies {
			if ok && possibleDep.ID == dep {
				return true
			}
		}
	}
	for possibleDepID := range q.running {
		log.Debug().Msgf("queue: running right now: %v", possibleDepID)
		if slices.Contains(task.Dependencies, possibleDepID) {
			return true
		}
	}
	return false
}

// expects the q to be currently owned e.g. locked by caller!
func (q *fifo) updateDepStatusInQueue(taskID string, status model.StatusValue) {
	for element := q.pending.Front(); element != nil; element = element.Next() {
		pending, _ := element.Value.(*model.Task)
		for _, dep := range pending.Dependencies {
			if taskID == dep {
				pending.DepStatus[dep] = status
			}
		}
	}

	for _, running := range q.running {
		for _, dep := range running.item.Dependencies {
			if taskID == dep {
				running.item.DepStatus[dep] = status
			}
		}
	}

	for element := q.waitingOnDeps.Front(); element != nil; element = element.Next() {
		waiting, _ := element.Value.(*model.Task)
		for _, dep := range waiting.Dependencies {
			if taskID == dep {
				waiting.DepStatus[dep] = status
			}
		}
	}
}

// expects the q to be currently owned e.g. locked by caller!
func (q *fifo) removeFromPendingAndWaiting(taskID string) error {
	log.Debug().Msgf("queue: trying to remove %s", taskID)

	// we assume pending first
	for element := q.pending.Front(); element != nil; element = element.Next() {
		task, _ := element.Value.(*model.Task)
		if task.ID == taskID {
			log.Debug().Msgf("queue: %s is removed from pending", taskID)
			_ = q.pending.Remove(element)
			return nil
		}
	}

	// well looks like it's waiting
	for element := q.waitingOnDeps.Front(); element != nil; element = element.Next() {
		task, _ := element.Value.(*model.Task)
		if task.ID == taskID {
			log.Debug().Msgf("queue: %s is removed from waitingOnDeps", taskID)
			_ = q.waitingOnDeps.Remove(element)
			return nil
		}
	}

	// well it could not be found
	return ErrNotFound
}
