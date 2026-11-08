import { describe, expect, it } from 'vitest';

import type { PipelineWorkflow } from '~/lib/api/types';

import { pipelineKinds, primaryPipelineKind, toPipelineKind, workflowKind } from './pipeline-kind';

function workflow(name: string, group?: string, kind?: PipelineWorkflow['kind']): PipelineWorkflow {
  return { id: 1, pipeline_id: 1, pid: 1, name, state: 'success', group, kind };
}

describe('toPipelineKind', () => {
  it('keeps the known kinds', () => {
    expect(toPipelineKind('build')).toBe('build');
    expect(toPipelineKind('test')).toBe('test');
    expect(toPipelineKind('deploy')).toBe('deploy');
    expect(toPipelineKind('other')).toBe('other');
  });

  it('ignores case and space', () => {
    expect(toPipelineKind(' Deploy ')).toBe('deploy');
  });

  it('maps unknown or missing groups to other', () => {
    expect(toPipelineKind('lint')).toBe('other');
    expect(toPipelineKind('')).toBe('other');
    expect(toPipelineKind(undefined)).toBe('other');
    expect(toPipelineKind(null)).toBe('other');
  });
});

describe('workflowKind', () => {
  it('prefers the declared kind over the group', () => {
    expect(workflowKind(workflow('module-lib-auth', 'build', 'test'))).toBe('test');
  });

  it('falls back to the group', () => {
    expect(workflowKind(workflow('deploy-service-tensor-rpc', 'deploy'))).toBe('deploy');
  });

  it('is other without kind or group', () => {
    expect(workflowKind(workflow('misc'))).toBe('other');
  });
});

describe('pipelineKinds', () => {
  it('uses the kinds the server sent, in display order', () => {
    expect(pipelineKinds({ kinds: ['deploy', 'build'], workflows: [workflow('x', 'test')] })).toEqual([
      'build',
      'deploy',
    ]);
  });

  it('derives the kinds from the workflows when the pipeline has none', () => {
    const workflows = [
      workflow('deploy-service-tensor-rpc', 'deploy'),
      workflow('test-service-knowledge-store-rpc', 'test'),
      workflow('test-service-tensor-rpc', 'test'),
      workflow('module-lib-auth', 'build'),
      workflow('custom', 'lint'),
    ];
    expect(pipelineKinds({ workflows })).toEqual(['build', 'test', 'deploy', 'other']);
    expect(pipelineKinds({ kinds: [], workflows })).toEqual(['build', 'test', 'deploy', 'other']);
  });

  it('is empty without kinds and workflows', () => {
    expect(pipelineKinds({})).toEqual([]);
    expect(pipelineKinds({ workflows: [] })).toEqual([]);
  });

  it('folds unknown kinds into other once', () => {
    expect(pipelineKinds({ kinds: ['lint', 'docs', 'other'] })).toEqual(['other']);
  });
});

describe('primaryPipelineKind', () => {
  it('ranks deploy over test over build over other', () => {
    expect(primaryPipelineKind({ kinds: ['build', 'test', 'deploy', 'other'] })).toBe('deploy');
    expect(primaryPipelineKind({ kinds: ['build', 'test', 'other'] })).toBe('test');
    expect(primaryPipelineKind({ kinds: ['build', 'other'] })).toBe('build');
    expect(primaryPipelineKind({ kinds: ['other'] })).toBe('other');
  });

  it('is undefined for a pipeline without workflows', () => {
    expect(primaryPipelineKind({})).toBeUndefined();
  });
});
