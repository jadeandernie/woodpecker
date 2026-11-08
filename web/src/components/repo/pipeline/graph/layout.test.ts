import { describe, expect, it } from 'vitest';

import type { PipelineStatus, PipelineWorkflow } from '~/lib/api/types';

import {
  buildGraph,
  clusterAnchor,
  computeDepths,
  defaultCollapsed,
  gateOf,
  resolveEdges,
  rollupStatus,
  workflowAnchor,
  workflowGate,
  workflowLane,
} from './layout';

let nextId = 1;

function wf(
  name: string,
  opts: { group?: string; subgroup?: string; depends_on?: string[]; state?: PipelineStatus } = {},
): PipelineWorkflow {
  const id = nextId++;
  return {
    id,
    pipeline_id: 1,
    pid: id,
    name,
    state: opts.state ?? 'success',
    group: opts.group,
    subgroup: opts.subgroup,
    depends_on: opts.depends_on,
  };
}

// a cognitiveos push that changed the tensor and knowledge services
function cognitiveosPipeline(): PipelineWorkflow[] {
  return [
    wf('gate-architecture', { group: 'test', subgroup: '' }),
    wf('gate-web-typecheck', { group: 'test', subgroup: 'web' }),
    wf('module-lib-rpc', { group: 'build', subgroup: 'rpc' }),
    wf('service-tensor-rpc', { group: 'build', subgroup: 'tensor' }),
    wf('service-tensor-worker', { group: 'build', subgroup: 'tensor' }),
    wf('svc-tensor', { group: 'build', subgroup: 'tensor' }),
    wf('service-knowledge-store-rpc', { group: 'build', subgroup: 'knowledge' }),
    wf('service-knowledge-search-rpc', { group: 'build', subgroup: 'knowledge', state: 'failure' }),
    wf('test-service-tensor-rpc', { group: 'test', subgroup: 'tensor' }),
    wf('test-service-tensor-worker', { group: 'test', subgroup: 'tensor', state: 'running' }),
    wf('publish-service-tensor-rpc', {
      group: 'deploy',
      subgroup: 'tensor',
      depends_on: ['service-tensor-rpc'],
    }),
    wf('publish-service-tensor-worker', {
      group: 'deploy',
      subgroup: 'tensor',
      depends_on: ['service-tensor-worker'],
      state: 'running',
    }),
    wf('deploy-service-tensor-rpc', {
      group: 'deploy',
      subgroup: 'tensor',
      depends_on: ['publish-service-tensor-rpc'],
      state: 'pending',
    }),
    wf('publish-service-knowledge-search-rpc', {
      group: 'deploy',
      subgroup: 'knowledge',
      depends_on: ['service-knowledge-search-rpc'],
      state: 'skipped',
    }),
    wf('deploy-service-knowledge-search-rpc', {
      group: 'deploy',
      subgroup: 'knowledge',
      depends_on: ['publish-service-knowledge-search-rpc'],
      state: 'skipped',
    }),
    wf('ci', { group: 'other' }),
    // staging's dependency is filtered out of this pipeline
    wf('staging', { depends_on: ['ci', 'nightly-tensor'], state: 'pending' }),
  ];
}

function names(workflows: { workflow: PipelineWorkflow }[]) {
  return workflows.map((node) => node.workflow.name);
}

describe('rollupStatus', () => {
  it('lets the worst state win', () => {
    expect(rollupStatus(['success', 'running', 'failure', 'pending'])).toBe('failure');
    expect(rollupStatus(['success', 'pending', 'running'])).toBe('running');
    expect(rollupStatus(['success', 'pending'])).toBe('pending');
    expect(rollupStatus(['success', 'skipped'])).toBe('success');
    expect(rollupStatus(['skipped'])).toBe('skipped');
    expect(rollupStatus([])).toBe('skipped');
    expect(rollupStatus(['error', 'killed'])).toBe('error');
  });
});

describe('workflowLane', () => {
  it('defaults missing and unknown groups to other', () => {
    expect(workflowLane({ group: 'build' })).toBe('build');
    expect(workflowLane({ group: 'Deploy' })).toBe('deploy');
    expect(workflowLane({})).toBe('other');
    expect(workflowLane({ group: 'lint' })).toBe('other');
    expect(workflowLane({ group: 'build', kind: 'deploy' })).toBe('deploy');
  });
});

describe('computeDepths', () => {
  it('places workflows one column after their deepest dependency', () => {
    const workflows = cognitiveosPipeline();
    const depths = computeDepths(workflows);
    const depthOf = (name: string) => depths.get(workflows.find((w) => w.name === name)!.id);
    expect(depthOf('service-tensor-rpc')).toBe(0);
    expect(depthOf('publish-service-tensor-rpc')).toBe(1);
    expect(depthOf('deploy-service-tensor-rpc')).toBe(2);
    expect(depthOf('staging')).toBe(1);
    expect(depthOf('gate-architecture')).toBe(0);
  });

  it('survives cycles and self references', () => {
    const workflows = [
      wf('a', { depends_on: ['b'] }),
      wf('b', { depends_on: ['a'] }),
      wf('c', { depends_on: ['c', 'b'] }),
    ];
    const depths = computeDepths(workflows);
    expect([...depths.values()].every((depth) => depth >= 0 && depth < 4)).toBe(true);
    expect(depths.get(workflows[2].id)).toBeGreaterThan(depths.get(workflows[1].id)!);
  });
});

describe('buildGraph', () => {
  it('lays out the cognitiveos pipeline in lanes, clusters and columns', () => {
    const layout = buildGraph(cognitiveosPipeline());

    expect(layout.columns).toBe(3);
    expect(layout.lanes.map((lane) => lane.lane)).toEqual(['build', 'test', 'deploy', 'other']);

    const build = layout.lanes[0];
    // module-lib-rpc is alone in its subgroup, so it is not a cluster
    expect(build.clusters.map((cluster) => cluster.subgroup)).toEqual(['', 'knowledge', 'tensor']);
    expect(names(build.clusters[0].nodes)).toEqual(['module-lib-rpc']);
    expect(names(build.clusters[2].nodes)).toEqual(['service-tensor-rpc', 'service-tensor-worker', 'svc-tensor']);
    expect(build.clusters[1].status).toBe('failure');
    expect(build.status).toBe('failure');
    expect(build.total).toBe(6);

    const test = layout.lanes[1];
    expect(test.clusters.map((cluster) => cluster.subgroup)).toEqual(['', 'tensor']);
    expect(names(test.clusters[0].nodes)).toEqual(['gate-architecture', 'gate-web-typecheck']);
    expect(test.status).toBe('running');
    expect(test.done).toBe(3);

    const deploy = layout.lanes[2];
    const tensor = deploy.clusters.find((cluster) => cluster.subgroup === 'tensor')!;
    expect(names(tensor.nodes)).toEqual([
      'publish-service-tensor-rpc',
      'publish-service-tensor-worker',
      'deploy-service-tensor-rpc',
    ]);
    expect([tensor.minDepth, tensor.maxDepth]).toEqual([1, 2]);
    expect(tensor.status).toBe('running');

    expect(layout.edges).toHaveLength(6);
  });

  it('keeps a pipeline without groups or dependencies in one column of one lane', () => {
    const layout = buildGraph([wf('lint'), wf('test'), wf('build')]);
    expect(layout.columns).toBe(1);
    expect(layout.lanes).toHaveLength(1);
    expect(layout.lanes[0].lane).toBe('other');
    expect(names(layout.lanes[0].clusters[0].nodes)).toEqual(['build', 'lint', 'test']);
    expect(layout.edges).toEqual([]);
  });

  it('handles an empty pipeline', () => {
    const layout = buildGraph([]);
    expect(layout.columns).toBe(1);
    expect(layout.lanes).toEqual([]);
  });

  it('connects every matrix instance of a dependency', () => {
    const workflows = [wf('build'), wf('build'), wf('deploy', { depends_on: ['build'] })];
    expect(buildGraph(workflows).edges).toHaveLength(2);
  });
});

describe('defaultCollapsed', () => {
  it('collapses big clusters that all went fine', () => {
    const many = Array.from({ length: 5 }, (_, i) =>
      wf(`module-lib-${i}`, { group: 'build', subgroup: 'lib', state: i === 0 ? 'skipped' : 'success' }),
    );
    expect(defaultCollapsed(buildGraph(many).lanes[0].clusters[0])).toBe(true);

    many[2].state = 'running';
    expect(defaultCollapsed(buildGraph(many).lanes[0].clusters[0])).toBe(false);

    expect(defaultCollapsed(buildGraph(many.slice(0, 2)).lanes[0].clusters[0])).toBe(false);
  });
});

describe('resolveEdges', () => {
  it('attaches edges to collapsed clusters and drops the ones inside', () => {
    const workflows = cognitiveosPipeline();
    const layout = buildGraph(workflows);
    const id = (name: string) => workflows.find((w) => w.name === name)!.id;

    const open = resolveEdges(layout, new Set());
    expect(open).toHaveLength(6);
    expect(open).toContainEqual(
      expect.objectContaining({
        from: workflowAnchor(id('service-knowledge-search-rpc')),
        to: workflowAnchor(id('publish-service-knowledge-search-rpc')),
        status: 'failure',
      }),
    );

    const collapsed = resolveEdges(layout, new Set(['deploy/tensor', 'build/tensor']));
    const tensorEdges = collapsed.filter((edge) => edge.from === clusterAnchor('build/tensor'));
    // both service -> publish edges merge into one cluster -> cluster edge; publish -> deploy is internal
    expect(tensorEdges).toEqual([expect.objectContaining({ to: clusterAnchor('deploy/tensor'), status: 'success' })]);
    expect(collapsed).toHaveLength(4);
  });
});

describe('gates', () => {
  it('explains why workflows wait or were skipped', () => {
    const workflows = cognitiveosPipeline();
    const find = (name: string) => workflows.find((w) => w.name === name)!;

    // its dependency has finished, so it only waits for an agent
    expect(workflowGate(find('deploy-service-tensor-rpc'), workflows)).toEqual({ kind: 'queued' });
    expect(workflowGate(find('publish-service-knowledge-search-rpc'), workflows)).toEqual({
      kind: 'skipped',
      on: 'service-knowledge-search-rpc',
      reason: 'failed',
    });
    expect(workflowGate(find('deploy-service-knowledge-search-rpc'), workflows)).toEqual({
      kind: 'skipped',
      on: 'publish-service-knowledge-search-rpc',
      reason: 'skipped',
    });
    // the missing nightly-tensor is ignored, ci has finished
    expect(workflowGate(find('staging'), workflows)).toEqual({ kind: 'queued' });
    expect(workflowGate(find('service-tensor-rpc'), workflows)).toBeUndefined();

    find('publish-service-tensor-rpc').state = 'running';
    expect(workflowGate(find('deploy-service-tensor-rpc'), workflows)).toEqual({
      kind: 'waiting',
      on: ['publish-service-tensor-rpc'],
    });
  });

  it('reports blocked and canceled dependencies', () => {
    const items = [
      { name: 'build', state: 'killed' as PipelineStatus },
      { name: 'deploy', state: 'skipped' as PipelineStatus, depends_on: ['build'] },
      { name: 'approve', state: 'blocked' as PipelineStatus },
    ];
    expect(gateOf(items[1], items)).toEqual({ kind: 'skipped', on: 'build', reason: 'canceled' });
    expect(gateOf(items[2], items)).toEqual({ kind: 'blocked' });
  });
});
