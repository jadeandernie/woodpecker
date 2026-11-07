import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';
import { createI18n } from 'vue-i18n';

import en from '~/assets/locales/en.json';
import type { Pipeline, PipelineStatus, PipelineWorkflow } from '~/lib/api/types';

import WorkflowGraph from './WorkflowGraph.vue';

const i18n = createI18n({ legacy: false, locale: 'en', messages: { en } });

let nextId = 1;
function wf(name: string, group: string, subgroup: string, state: PipelineStatus, depends_on?: string[]) {
  const id = nextId++;
  return { id, pipeline_id: 1, pid: id, name, state, group, subgroup, depends_on } satisfies PipelineWorkflow;
}

function pipeline(workflows: PipelineWorkflow[]): Pipeline {
  return { id: 1, number: 7, status: 'running', workflows } as Pipeline;
}

function mountGraph(workflows: PipelineWorkflow[]) {
  return mount(WorkflowGraph, {
    props: { pipeline: pipeline(workflows) },
    global: { plugins: [i18n], stubs: { RouterLink: { template: '<a><slot /></a>' } } },
  });
}

describe('workflowGraph', () => {
  it('shows lanes, gates and collapsed clusters', async () => {
    const libs = ['acmg', 'auth', 'curie', 'embed', 'eval'].map((lib) =>
      wf(`module-lib-${lib}`, 'build', 'lib', 'success'),
    );
    const wrapper = mountGraph([
      ...libs,
      wf('service-knowledge-search-rpc', 'build', 'knowledge', 'failure'),
      wf('service-knowledge-store-rpc', 'build', 'knowledge', 'running'),
      wf('publish-service-knowledge-search-rpc', 'deploy', 'knowledge', 'skipped', ['service-knowledge-search-rpc']),
      wf('publish-service-knowledge-store-rpc', 'deploy', 'knowledge', 'pending', ['service-knowledge-store-rpc']),
      wf('gate-architecture', 'test', '', 'success'),
    ]);

    const text = wrapper.text();
    expect(text).toContain('Build');
    expect(text).toContain('Test');
    expect(text).toContain('Deploy');
    expect(text).not.toContain('Other');
    expect(text).toContain('Skipped: service-knowledge-search-rpc failed');
    expect(text).toContain('Waiting for service-knowledge-store-rpc');

    // the five successful libs are folded into one card
    expect(text).not.toContain('module-lib-acmg');
    expect(wrapper.find('[data-graph-anchor="c:build/lib"]').exists()).toBe(true);

    await wrapper.find('[data-graph-anchor="c:build/lib"]').trigger('click');
    expect(wrapper.text()).toContain('module-lib-acmg');
    expect(wrapper.find('[data-graph-anchor="c:build/lib"]').exists()).toBe(false);
  });

  it('says so when there are no workflows', () => {
    expect(mountGraph([]).text()).toContain('This pipeline has no workflows.');
  });
});
