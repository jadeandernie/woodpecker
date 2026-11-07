import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';
import { nextTick } from 'vue';
import { createI18n } from 'vue-i18n';
import { createMemoryHistory, createRouter } from 'vue-router';

import en from '~/assets/locales/en.json';
import type { PipelineStatus, StatsPipeline, StatsWorkflow } from '~/lib/api/types';

import InsightsChartPanel from './InsightsChartPanel.vue';
import InsightsHistoryGrid from './InsightsHistoryGrid.vue';
import { buildHistoryRows, chronological, formatDuration, summarize } from './stats';

const i18n = createI18n({ legacy: false, locale: 'en', messages: { en } });

const router = createRouter({
  history: createMemoryHistory(),
  routes: [
    { path: '/', component: { template: '<div />' } },
    { path: '/p/:pipelineId', name: 'repo-pipeline', component: { template: '<div />' } },
    { path: '/p/:pipelineId/graph', name: 'repo-pipeline-graph', component: { template: '<div />' } },
  ],
});

const global = { plugins: [i18n, router] };

function wf(id: number, name: string, group: string, subgroup: string, state: PipelineStatus): StatsWorkflow {
  return { id, name, group, subgroup, state };
}

// knowledge fails on each of its runs, tensor only runs now and then and passes
const pipelines: StatsPipeline[] = [1, 2, 3, 4].map((number) => ({
  id: number,
  number,
  status: 'failure',
  event: 'push',
  branch: 'main',
  commit: `abc${number}`,
  message: `change ${number}`,
  created: 1_700_000_000 + number,
  workflows: [
    wf(number * 10, 'svc-knowledge', 'build', 'knowledge', 'failure'),
    ...(number % 2 === 0 ? [wf(number * 10 + 1, 'svc-tensor', 'build', 'tensor', 'success')] : []),
  ],
}));

describe('insightsHistoryGrid', () => {
  const columns = chronological(pipelines);
  const rows = buildHistoryRows(columns);

  it('draws one square per pipeline and opens groups with a failing subgroup', async () => {
    const wrapper = mount(InsightsHistoryGrid, {
      props: { columns, rows, groupLabel: (g: string) => g.toUpperCase() },
      global,
    });
    await nextTick();

    const links = wrapper.findAll('a');
    // 4 pipeline squares + 4 build cells + 4 knowledge cells + 2 tensor cells
    expect(links).toHaveLength(14);
    expect(links[0].attributes('href')).toBe('/p/1');
    expect(links[4].attributes('href')).toBe('/p/1/graph');

    const text = wrapper.text();
    expect(text).toContain('BUILD');
    expect(text).toContain('knowledge');
    expect(text).toContain('failing for 4 runs');
    expect(text.indexOf('knowledge')).toBeLessThan(text.indexOf('tensor'));

    // collapsing the group hides its subgroups
    await wrapper.find('button[aria-expanded="true"]').trigger('click');
    expect(wrapper.text()).not.toContain('tensor');
  });
});

describe('insightsChartPanel', () => {
  it('shows the latest value, median and p90', () => {
    const values = [60, 120, 180, 240];
    const wrapper = mount(InsightsChartPanel, {
      props: {
        title: 'Runtime',
        summary: summarize(values),
        points: values.map((value, i) => ({ number: i + 1, value, message: '', time: '' })),
        unit: 'duration',
        format: formatDuration,
      },
      global,
    });
    const text = wrapper.text();
    expect(text).toContain('Runtime');
    expect(text).toContain('4m'); // latest
    expect(text).toContain('2m 30s'); // median
    expect(text).toContain('3m 42s'); // p90
  });
});
