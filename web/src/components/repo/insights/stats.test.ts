import { describe, expect, it } from 'vitest';

import type { PipelineStatus, StatsPipeline, StatsWorkflow } from '~/lib/api/types';

import {
  buildHistoryRows,
  chronological,
  failingStreak,
  formatBytes,
  formatDuration,
  groupRuntime,
  groupTimings,
  niceTicks,
  percentile,
  pipelineMemory,
  pipelineQueued,
  pipelineRuntime,
  rollUpStatus,
  summarize,
} from './stats';

// Workflow names and the groups the server's built-in rules give them, taken
// from the cognitiveos .woodpecker directory.
const catalog: Record<string, [string, string]> = {
  'svc-knowledge': ['build', 'knowledge'],
  'service-knowledge-store-rpc': ['build', 'knowledge'],
  'module-lib-tensor': ['build', 'tensor'],
  'svc-tensor': ['build', 'tensor'],
  'test-service-knowledge-store-rpc': ['test', 'knowledge'],
  'test-service-tensor-rpc': ['test', 'tensor'],
  'nightly-tensor': ['test', 'tensor'],
  'gate-lint': ['test', 'lint'],
  'deploy-service-knowledge-store-rpc': ['deploy', 'knowledge'],
  staging: ['deploy', 'staging'],
  ci: ['other', ''],
};

let nextId = 1;

function wf(name: string, state: PipelineStatus, started = 0, finished = 0, memory = 0): StatsWorkflow {
  const [group, subgroup] = catalog[name];
  return { id: nextId++, name, group, subgroup, state, started, finished, memory_peak: memory };
}

function pipeline(
  number: number,
  status: PipelineStatus,
  workflows: StatsWorkflow[],
  extra: Partial<StatsPipeline> = {},
): StatsPipeline {
  const created = 1_700_000_000 + number * 1000;
  return {
    id: 100 + number,
    number,
    status,
    event: 'push',
    branch: 'main',
    commit: `c0ffee${number}`,
    message: `change ${number}`,
    created,
    started: created + 30,
    finished: created + 630,
    memory_peak: 0,
    workflows,
    ...extra,
  };
}

// Builds run per changed service: knowledge fails on each of its last runs,
// tensor recovers, staging deploys only now and then. Newest first, like the API.
function history(): StatsPipeline[] {
  return [
    pipeline(7, 'running', [wf('ci', 'success', 1, 2), wf('svc-tensor', 'running', 1)], { finished: 0 }),
    pipeline(6, 'failure', [
      wf('ci', 'success'),
      wf('svc-knowledge', 'failure'),
      wf('test-service-knowledge-store-rpc', 'skipped'),
    ]),
    pipeline(5, 'success', [wf('ci', 'success'), wf('module-lib-tensor', 'success'), wf('nightly-tensor', 'success')]),
    pipeline(4, 'failure', [
      wf('ci', 'success'),
      wf('svc-knowledge', 'success'),
      wf('test-service-knowledge-store-rpc', 'failure'),
      wf('deploy-service-knowledge-store-rpc', 'skipped'),
    ]),
    pipeline(3, 'killed', [wf('ci', 'success'), wf('svc-tensor', 'killed'), wf('test-service-tensor-rpc', 'canceled')]),
    pipeline(2, 'failure', [wf('ci', 'success'), wf('svc-knowledge', 'error'), wf('staging', 'success')]),
    pipeline(1, 'success', [wf('ci', 'success'), wf('svc-knowledge', 'success'), wf('gate-lint', 'success')]),
  ];
}

describe('rollUpStatus', () => {
  it('lets the worst state win', () => {
    expect(rollUpStatus(['success', 'failure', 'running'])).toBe('failure');
    expect(rollUpStatus(['success', 'killed'])).toBe('killed');
    expect(rollUpStatus(['success', 'pending', 'running'])).toBe('running');
    expect(rollUpStatus(['success', 'blocked'])).toBe('blocked');
    expect(rollUpStatus(['skipped', 'success'])).toBe('success');
    expect(rollUpStatus(['skipped'])).toBe('skipped');
    expect(rollUpStatus([])).toBeUndefined();
  });
});

describe('failingStreak', () => {
  it('counts failures from the newest run, stepping over running and canceled ones', () => {
    expect(failingStreak(['success', 'failure', 'failure'])).toBe(2);
    expect(failingStreak(['failure', 'failure', 'canceled', 'failure', 'running'])).toBe(3);
    expect(failingStreak(['failure', 'success'])).toBe(0);
    expect(failingStreak([])).toBe(0);
  });
});

describe('buildHistoryRows', () => {
  const columns = chronological(history());
  const rows = buildHistoryRows(columns);
  const row = (key: string) => rows.flatMap((r) => [r, ...r.children]).find((r) => r.key === key)!;

  it('orders columns oldest first', () => {
    expect(columns.map((p) => p.number)).toEqual([1, 2, 3, 4, 5, 6, 7]);
  });

  it('orders groups build, test, deploy, other', () => {
    expect(rows.map((r) => r.group)).toEqual(['build', 'test', 'deploy', 'other']);
  });

  it('leaves cells empty where the pipeline did not run the subgroup', () => {
    const knowledge = row('build/knowledge');
    expect(knowledge.cells.map((c) => c?.status)).toEqual([
      'success',
      'error',
      undefined,
      'success',
      undefined,
      'failure',
      undefined,
    ]);
    expect(knowledge.runs).toBe(4);
    expect(knowledge.current?.pipeline.number).toBe(6);
  });

  it('shows knowledge failing across build and test while tensor recovered', () => {
    expect(row('build/knowledge').failingStreak).toBe(1);
    expect(row('test/knowledge').failingStreak).toBe(1);
    expect(row('test/knowledge').current?.status).toBe('skipped');
    // tensor: killed in #3, success in #5, running in #7
    const tensor = row('build/tensor');
    expect(tensor.failingStreak).toBe(0);
    expect(tensor.current?.kind).toBe('running');
    expect(tensor.passRate).toBe(0.5);
  });

  it('rolls subgroups up into the group row', () => {
    const build = row('build');
    expect(build.cells.map((c) => c?.kind)).toEqual([
      'success',
      'failure',
      'failure',
      'success',
      'success',
      'failure',
      'running',
    ]);
    expect(build.passRate).toBe(0.5);
    expect(build.failingStreak).toBe(1);
  });

  it('sorts failing subgroups first', () => {
    expect(row('build').children.map((r) => r.subgroup)).toEqual(['knowledge', 'tensor']);
    expect(row('test').children.map((r) => r.subgroup)).toEqual(['knowledge', 'lint', 'tensor']);
  });

  it('treats a missing group as other and a missing subgroup as empty', () => {
    const p = pipeline(1, 'success', [
      { id: 1, name: 'x', group: '', subgroup: undefined as unknown as string, state: 'success' },
    ]);
    const r = buildHistoryRows([p]);
    expect(r.map((g) => g.group)).toEqual(['other']);
    expect(r[0].children.map((c) => c.subgroup)).toEqual(['']);
  });

  it('puts a long failing streak on top', () => {
    const ps = [1, 2, 3].map((n) =>
      pipeline(n, 'failure', [wf('svc-tensor', 'success'), wf('svc-knowledge', 'failure')]),
    );
    const r = buildHistoryRows(ps);
    expect(r[0].children[0].subgroup).toBe('knowledge');
    expect(r[0].children[0].failingStreak).toBe(3);
    expect(r[0].children[0].passRate).toBe(0);
  });
});

describe('timings', () => {
  const now = 1_700_100_000;

  it('computes runtime from started to finished', () => {
    expect(pipelineRuntime(pipeline(1, 'success', []))).toBe(600);
    expect(pipelineRuntime(pipeline(1, 'running', [], { finished: 0 }))).toBeUndefined();
  });

  it('computes queued time from creation to the first workflow start', () => {
    const p = pipeline(1, 'success', [wf('ci', 'success', 0, 0), wf('svc-tensor', 'success', 0, 0)]);
    p.workflows[0].started = p.created + 45;
    p.workflows[1].started = p.created + 12;
    expect(pipelineQueued(p, now)).toBe(12);
  });

  it('falls back to the pipeline start without workflow timestamps', () => {
    expect(pipelineQueued(pipeline(1, 'success', []), now)).toBe(30);
  });

  it('counts a pending pipeline as waiting until now', () => {
    const p = pipeline(1, 'pending', [wf('ci', 'pending')], { started: 0, finished: 0 });
    expect(pipelineQueued(p, p.created + 90)).toBe(90);
  });

  it('has no queued time for a pipeline that never started', () => {
    expect(pipelineQueued(pipeline(1, 'skipped', [], { started: 0, finished: 0 }), now)).toBeUndefined();
  });

  it('treats 0 memory as not measured', () => {
    expect(pipelineMemory(pipeline(1, 'success', []))).toBeUndefined();
    expect(pipelineMemory(pipeline(1, 'success', [], { memory_peak: 512 * 1024 * 1024 }))).toBe(536870912);
  });

  it('measures a group from its first start to its last finish', () => {
    expect(groupRuntime([wf('svc-tensor', 'success', 100, 160), wf('module-lib-tensor', 'success', 110, 250)])).toBe(
      150,
    );
    expect(groupRuntime([wf('svc-tensor', 'running', 100, 0)])).toBeUndefined();
    expect(groupRuntime([wf('svc-tensor', 'skipped', 0, 0)])).toBeUndefined();
  });

  it('summarizes group runtimes per group', () => {
    const ps = [1, 2].map((n) => pipeline(n, 'success', [wf('svc-tensor', 'success', 100, 100 + n * 60)]));
    const [build] = groupTimings(buildHistoryRows(ps));
    expect(build.values).toEqual([60, 120]);
    expect(build.summary.latest).toBe(120);
  });
});

describe('statistics', () => {
  it('interpolates percentiles', () => {
    expect(percentile([], 0.5)).toBeUndefined();
    expect(percentile([5], 0.9)).toBe(5);
    expect(percentile([1, 2, 3, 4], 0.5)).toBe(2.5);
    expect(percentile([10, 1, 7, 3, 5, 9, 2, 8, 4, 6], 0.9)).toBeCloseTo(9.1);
  });

  it('summarizes measured values, skipping gaps', () => {
    expect(summarize([undefined, 30, 10, undefined, 20])).toEqual({ latest: 20, median: 20, p90: 28, count: 3 });
    expect(summarize([])).toEqual({ latest: undefined, median: undefined, p90: undefined, count: 0 });
  });
});

describe('formatting', () => {
  it('formats durations', () => {
    expect(formatDuration(undefined)).toBe('–');
    expect(formatDuration(42)).toBe('42s');
    expect(formatDuration(65)).toBe('1m 05s');
    expect(formatDuration(600)).toBe('10m');
    expect(formatDuration(3720)).toBe('1h 2m');
  });

  it('formats bytes as MiB and GiB', () => {
    expect(formatBytes(undefined)).toBe('–');
    expect(formatBytes(0)).toBe('0');
    expect(formatBytes(512 * 1024)).toBe('512 KiB');
    expect(formatBytes(1.5 * 1024 * 1024)).toBe('1.5 MiB');
    expect(formatBytes(300 * 1024 * 1024)).toBe('300 MiB');
    expect(formatBytes(2.25 * 1024 * 1024 * 1024)).toBe('2.3 GiB');
  });

  it('picks round axis ticks', () => {
    expect(niceTicks(0, 'duration')).toEqual([0]);
    expect(niceTicks(600, 'duration')).toEqual([0, 300, 600]);
    expect(niceTicks(1000, 'duration')).toEqual([0, 300, 600, 900, 1200]);
    expect(niceTicks(700 * 1024 * 1024, 'bytes')).toEqual([0, 256, 512, 768].map((n) => n * 1024 * 1024));
  });
});
