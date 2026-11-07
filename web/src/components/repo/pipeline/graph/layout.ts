// Layout of a pipeline's workflows as a run graph: workflows are placed in columns by their depends_on depth and in
// lanes by their group (build, test, deploy, other); workflows of one lane that share a subgroup form a cluster.
// The group and subgroup come from the server; this module only arranges what it is given.

import type { PipelineStatus, PipelineStep, PipelineWorkflow } from '~/lib/api/types';

export const LANES = ['build', 'test', 'deploy', 'other'] as const;
export type Lane = (typeof LANES)[number];

export interface GraphNode {
  workflow: PipelineWorkflow;
  depth: number;
  lane: Lane;
  // the cluster this node is shown in
  clusterKey: string;
}

export interface GraphCluster {
  key: string;
  lane: Lane;
  // '' for the workflows of a lane that are not part of a named cluster
  subgroup: string;
  nodes: GraphNode[];
  status: PipelineStatus;
  minDepth: number;
  maxDepth: number;
  // number of finished nodes
  done: number;
}

export interface GraphLane {
  lane: Lane;
  clusters: GraphCluster[];
  status: PipelineStatus;
  total: number;
  done: number;
}

// an edge from a workflow to a workflow that depends on it, by workflow id
export interface GraphEdge {
  from: number;
  to: number;
}

export interface GraphLayout {
  lanes: GraphLane[];
  nodes: Map<number, GraphNode>;
  edges: GraphEdge[];
  // number of depth columns, at least 1
  columns: number;
}

// a connector between two rendered anchors (a workflow card or a collapsed cluster)
export interface ResolvedEdge {
  key: string;
  from: string;
  to: string;
  // the roll-up state of the source workflow(s)
  status: PipelineStatus;
}

const finishedStates: PipelineStatus[] = ['success', 'failure', 'error', 'killed', 'skipped', 'declined', 'canceled'];
const failedStates: PipelineStatus[] = ['failure', 'error', 'declined'];
const canceledStates: PipelineStatus[] = ['killed', 'canceled'];

export function isFinished(state: PipelineStatus): boolean {
  return finishedStates.includes(state);
}

export function isRunning(state: PipelineStatus): boolean {
  return state === 'running' || state === 'started';
}

// severity of a state for roll-ups: failure > canceled > running > pending > success > skipped
function severity(state: PipelineStatus): number {
  if (failedStates.includes(state)) {
    return 6;
  }
  if (canceledStates.includes(state)) {
    return 5;
  }
  if (isRunning(state)) {
    return 4;
  }
  if (state === 'pending' || state === 'blocked') {
    return 3;
  }
  if (state === 'success') {
    return 2;
  }
  return 1;
}

// rollupStatus returns the worst of the given states; an empty list counts as skipped
export function rollupStatus(states: PipelineStatus[]): PipelineStatus {
  let worst: PipelineStatus = 'skipped';
  for (const state of states) {
    if (severity(state) > severity(worst)) {
      worst = state;
    }
  }
  return worst;
}

export function workflowLane(workflow: Pick<PipelineWorkflow, 'group'>): Lane {
  const group = workflow.group?.toLowerCase() ?? '';
  return (LANES as readonly string[]).includes(group) ? (group as Lane) : 'other';
}

function byName<T extends { name: string }>(items: T[]): Map<string, T[]> {
  const map = new Map<string, T[]>();
  for (const item of items) {
    const list = map.get(item.name) ?? [];
    list.push(item);
    map.set(item.name, list);
  }
  return map;
}

// computeDepths gives each workflow (by id) its column: 0 without dependencies in this pipeline, else one more than
// its deepest dependency. Dependencies that are not part of the pipeline are ignored and cycles are cut.
export function computeDepths(workflows: PipelineWorkflow[]): Map<number, number> {
  const named = byName(workflows);
  const depths = new Map<number, number>();
  const visiting = new Set<number>();

  function depthOf(workflow: PipelineWorkflow): number {
    const known = depths.get(workflow.id);
    if (known !== undefined) {
      return known;
    }
    if (visiting.has(workflow.id)) {
      return -1; // cycle: do not count this dependency
    }
    visiting.add(workflow.id);
    let depth = 0;
    for (const dep of workflow.depends_on ?? []) {
      for (const parent of named.get(dep) ?? []) {
        if (parent.id !== workflow.id) {
          depth = Math.max(depth, depthOf(parent) + 1);
        }
      }
    }
    visiting.delete(workflow.id);
    depths.set(workflow.id, depth);
    return depth;
  }

  workflows.forEach(depthOf);
  return depths;
}

export function clusterKey(lane: Lane, subgroup: string): string {
  return `${lane}/${subgroup}`;
}

function compareNodes(a: GraphNode, b: GraphNode): number {
  return a.depth - b.depth || a.workflow.name.localeCompare(b.workflow.name) || a.workflow.id - b.workflow.id;
}

// buildGraph arranges the workflows of a pipeline. Subgroups with a single workflow in a lane are not worth a
// cluster of their own and join the lane's unclustered workflows.
export function buildGraph(workflows: PipelineWorkflow[]): GraphLayout {
  const depths = computeDepths(workflows);
  const named = byName(workflows);

  const subgroupCounts = new Map<string, number>();
  for (const workflow of workflows) {
    const key = clusterKey(workflowLane(workflow), workflow.subgroup ?? '');
    subgroupCounts.set(key, (subgroupCounts.get(key) ?? 0) + 1);
  }

  const nodes = new Map<number, GraphNode>();
  const clusters = new Map<string, GraphCluster>();
  for (const workflow of workflows) {
    const lane = workflowLane(workflow);
    let subgroup = workflow.subgroup ?? '';
    if ((subgroupCounts.get(clusterKey(lane, subgroup)) ?? 0) < 2) {
      subgroup = '';
    }
    const key = clusterKey(lane, subgroup);
    const node: GraphNode = { workflow, depth: depths.get(workflow.id) ?? 0, lane, clusterKey: key };
    nodes.set(workflow.id, node);

    let cluster = clusters.get(key);
    if (!cluster) {
      cluster = {
        key,
        lane,
        subgroup,
        nodes: [],
        status: 'skipped',
        minDepth: node.depth,
        maxDepth: node.depth,
        done: 0,
      };
      clusters.set(key, cluster);
    }
    cluster.nodes.push(node);
  }

  for (const cluster of clusters.values()) {
    cluster.nodes.sort(compareNodes);
    cluster.status = rollupStatus(cluster.nodes.map((node) => node.workflow.state));
    cluster.minDepth = Math.min(...cluster.nodes.map((node) => node.depth));
    cluster.maxDepth = Math.max(...cluster.nodes.map((node) => node.depth));
    cluster.done = cluster.nodes.filter((node) => isFinished(node.workflow.state)).length;
  }

  const lanes: GraphLane[] = [];
  for (const lane of LANES) {
    const laneClusters = [...clusters.values()]
      .filter((cluster) => cluster.lane === lane)
      // the unclustered workflows first, then the clusters by name
      .sort((a, b) => (a.subgroup === '' ? -1 : b.subgroup === '' ? 1 : a.subgroup.localeCompare(b.subgroup)));
    if (laneClusters.length === 0) {
      continue;
    }
    const laneNodes = laneClusters.flatMap((cluster) => cluster.nodes);
    lanes.push({
      lane,
      clusters: laneClusters,
      status: rollupStatus(laneNodes.map((node) => node.workflow.state)),
      total: laneNodes.length,
      done: laneNodes.filter((node) => isFinished(node.workflow.state)).length,
    });
  }

  const edges: GraphEdge[] = [];
  for (const workflow of workflows) {
    for (const dep of new Set(workflow.depends_on ?? [])) {
      for (const parent of named.get(dep) ?? []) {
        if (parent.id !== workflow.id) {
          edges.push({ from: parent.id, to: workflow.id });
        }
      }
    }
  }

  const columns = Math.max(1, ...[...depths.values()].map((depth) => depth + 1));
  return { lanes, nodes, edges, columns };
}

// a cluster starts collapsed when it is big and everything in it went fine
export const COLLAPSE_MIN_SIZE = 4;

export function defaultCollapsed(cluster: GraphCluster): boolean {
  return (
    cluster.subgroup !== '' &&
    cluster.nodes.length >= COLLAPSE_MIN_SIZE &&
    cluster.nodes.every((node) => node.workflow.state === 'success' || node.workflow.state === 'skipped')
  );
}

export function workflowAnchor(id: number): string {
  return `w:${id}`;
}

export function clusterAnchor(key: string): string {
  return `c:${key}`;
}

// resolveEdges maps the workflow edges onto what is rendered: edges into or out of a collapsed cluster attach to the
// cluster, edges inside one disappear and duplicates are merged.
export function resolveEdges(layout: GraphLayout, collapsed: Set<string>): ResolvedEdge[] {
  function anchorOf(id: number): string | undefined {
    const node = layout.nodes.get(id);
    if (!node) {
      return undefined;
    }
    return collapsed.has(node.clusterKey) ? clusterAnchor(node.clusterKey) : workflowAnchor(id);
  }

  const merged = new Map<string, { from: string; to: string; states: PipelineStatus[] }>();
  for (const edge of layout.edges) {
    const from = anchorOf(edge.from);
    const to = anchorOf(edge.to);
    if (from === undefined || to === undefined || from === to) {
      continue;
    }
    const key = `${from}>${to}`;
    const entry = merged.get(key) ?? { from, to, states: [] };
    entry.states.push(layout.nodes.get(edge.from)!.workflow.state);
    merged.set(key, entry);
  }

  return [...merged.entries()].map(([key, { from, to, states }]) => ({
    key,
    from,
    to,
    status: rollupStatus(states),
  }));
}

// A gate explains why a workflow has not run (yet).
export type Gate =
  | { kind: 'waiting'; on: string[] }
  | { kind: 'blocked' }
  | { kind: 'skipped'; on: string; reason: 'failed' | 'canceled' | 'skipped' }
  | { kind: 'queued' };

// workflowGate tells what a pending, blocked or skipped workflow is waiting for or was stopped by. Steps use the same
// rule with their sibling steps, so it takes anything with a name, a state and dependencies.
export function gateOf<T extends { name: string; state: PipelineStatus; depends_on?: string[] }>(
  item: T,
  siblings: T[],
): Gate | undefined {
  const named = byName(siblings);
  const deps = [...new Set(item.depends_on ?? [])].filter((dep) => named.has(dep));
  const depState = (dep: string) => rollupStatus(named.get(dep)!.map((sibling) => sibling.state));

  if (item.state === 'skipped') {
    for (const [states, reason] of [
      [failedStates, 'failed'],
      [canceledStates, 'canceled'],
      [['skipped'], 'skipped'],
    ] as const) {
      const culprit = deps.find((dep) => (states as readonly PipelineStatus[]).includes(depState(dep)));
      if (culprit !== undefined) {
        return { kind: 'skipped', on: culprit, reason };
      }
    }
    return undefined;
  }

  if (item.state === 'blocked') {
    return { kind: 'blocked' };
  }

  if (item.state === 'pending') {
    const waiting = deps.filter((dep) => !isFinished(depState(dep)));
    return waiting.length > 0 ? { kind: 'waiting', on: waiting } : { kind: 'queued' };
  }

  return undefined;
}

export function workflowGate(workflow: PipelineWorkflow, workflows: PipelineWorkflow[]): Gate | undefined {
  return gateOf(workflow, workflows);
}

export function stepGate(step: PipelineStep, steps: PipelineStep[]): Gate | undefined {
  return gateOf(step, steps);
}

// stepProgress counts a workflow's steps by how far they got
export function stepProgress(workflow: PipelineWorkflow): { done: number; total: number } {
  const steps = workflow.children ?? [];
  return { done: steps.filter((step) => isFinished(step.state)).length, total: steps.length };
}
