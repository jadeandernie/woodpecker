<template>
  <div class="flex flex-col gap-4">
    <!-- filters, kept in the URL so a view can be shared -->
    <div class="flex flex-wrap items-end gap-2">
      <label class="text-wp-text-100 flex max-w-full min-w-0 flex-col gap-1 text-sm">
        {{ $t('repo.insights.branch') }}
        <SelectField v-model="branch" :options="branchOptions" class="min-w-40 md:min-w-56" />
      </label>
      <label class="text-wp-text-100 flex flex-col gap-1 text-sm">
        {{ $t('repo.insights.event') }}
        <SelectField v-model="event" :options="eventOptions" class="min-w-36" />
      </label>
      <label class="text-wp-text-100 flex flex-col gap-1 text-sm">
        {{ $t('repo.insights.window') }}
        <SelectField v-model="limit" :options="limitOptions" class="min-w-36" />
      </label>
      <IconButton
        icon="refresh"
        class="mb-0.5 h-8 w-8"
        :title="$t('repo.insights.reload')"
        :is-loading="loading"
        @click="load()"
      />
    </div>

    <Panel v-if="loadError">
      <span class="text-wp-error-100">{{ $t('repo.insights.load_error', { error: loadError }) }}</span>
    </Panel>

    <div v-else-if="!stats" class="text-wp-text-100 flex justify-center">
      <Icon name="spinner" class="animate-spin" />
    </div>

    <Panel v-else-if="columns.length === 0">
      <span class="text-wp-text-100">{{ $t('repo.insights.no_pipelines') }}</span>
    </Panel>

    <template v-else>
      <!-- headline: where the branch stands now -->
      <div class="grid grid-cols-1 gap-4 md:grid-cols-3">
        <div :class="tileClass">
          <div :class="tileTitleClass">{{ $t('repo.insights.now') }}</div>
          <router-link
            v-if="latest"
            :to="{ name: 'repo-pipeline', params: { pipelineId: latest.number } }"
            class="flex min-w-0 items-center gap-2 hover:underline"
          >
            <PipelineStatusIcon :status="latest.status" />
            <div class="min-w-0">
              <div class="truncate font-bold" :class="kindTextClass[statusKind(latest.status)]">
                {{ $t('repo.insights.pipeline_status', { number: latest.number, status: latest.status }) }}
              </div>
              <div class="text-wp-text-100 truncate text-sm" :title="latest.message">
                {{ firstLine(latest.message) }}
              </div>
              <div class="text-wp-text-alt-100 text-xs">{{ timeAgo(latest.created * 1000) }}</div>
            </div>
          </router-link>
        </div>

        <div :class="tileClass">
          <div :class="tileTitleClass">{{ $t('repo.insights.pass_rate') }}</div>
          <div class="text-wp-text-200 text-2xl font-bold">{{ formatPercent(branchPassRate.rate) }}</div>
          <div class="text-wp-text-alt-100 text-xs">
            {{ $t('repo.insights.passed_of', { passed: branchPassRate.passed, total: branchPassRate.total }) }}
          </div>
        </div>

        <div :class="tileClass">
          <div :class="tileTitleClass">{{ $t('repo.insights.failing_now') }}</div>
          <div v-if="failing.length === 0" class="text-wp-state-ok-100 font-bold">
            {{ $t('repo.insights.nothing_failing') }}
          </div>
          <ul v-else class="flex flex-col gap-0.5 text-sm">
            <li v-for="row in failing.slice(0, 5)" :key="row.key" class="flex min-w-0 gap-2">
              <span class="text-wp-text-200 truncate font-bold">
                {{ row.subgroup || $t('repo.insights.no_subgroup') }}
              </span>
              <span class="text-wp-text-alt-100 shrink-0">{{ groupLabel(row.group) }}</span>
              <span class="text-wp-error-100 ml-auto shrink-0">
                {{ $t('repo.insights.failing_for', { count: row.failingStreak }, row.failingStreak) }}
              </span>
            </li>
            <li v-if="failing.length > 5" class="text-wp-text-alt-100 text-xs">
              {{ $t('repo.insights.and_more', { count: failing.length - 5 }) }}
            </li>
          </ul>
        </div>
      </div>

      <Panel :title="$t('repo.insights.history')">
        <InsightsHistoryGrid :columns="columns" :rows="rows" :group-label="groupLabel" />
      </Panel>

      <div class="grid grid-cols-1 gap-4 xl:grid-cols-2">
        <InsightsChartPanel
          :title="$t('repo.insights.runtime')"
          :summary="runtime.summary"
          :points="runtime.points"
          unit="duration"
          :format="formatDuration"
          @select="openPipeline"
        />
        <InsightsChartPanel
          :title="$t('repo.insights.queued')"
          :summary="queued.summary"
          :points="queued.points"
          unit="duration"
          :format="formatDuration"
          @select="openPipeline"
        >
          {{ $t('repo.insights.queued_desc') }}
        </InsightsChartPanel>
        <InsightsChartPanel
          :title="$t('repo.insights.memory')"
          :summary="memory.summary"
          :points="memory.points"
          unit="bytes"
          :format="formatBytes"
          @select="openPipeline"
        >
          <span v-if="memory.summary.count === 0">{{ $t('repo.insights.memory_none') }}</span>
          <span v-else-if="memory.summary.count < columns.length">
            {{ $t('repo.insights.memory_partial', { missing: columns.length - memory.summary.count }) }}
          </span>
        </InsightsChartPanel>

        <Panel :title="$t('repo.insights.group_runtime')">
          <div class="overflow-x-auto">
            <table class="w-full text-sm">
              <thead>
                <tr class="text-wp-text-alt-100 text-left text-xs">
                  <th class="py-1 font-normal">{{ $t('repo.insights.group') }}</th>
                  <th class="py-1 text-right font-normal">{{ $t('repo.insights.latest') }}</th>
                  <th class="py-1 text-right font-normal">{{ $t('repo.insights.median') }}</th>
                  <th class="py-1 text-right font-normal">{{ $t('repo.insights.p90') }}</th>
                  <th class="py-1 text-right font-normal">{{ $t('repo.insights.measured_runs') }}</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="timing in groupRuntimes" :key="timing.group" class="border-wp-background-400 border-t">
                  <td class="py-1">{{ groupLabel(timing.group) }}</td>
                  <td class="py-1 text-right tabular-nums">{{ formatDuration(timing.summary.latest) }}</td>
                  <td class="py-1 text-right tabular-nums">{{ formatDuration(timing.summary.median) }}</td>
                  <td class="py-1 text-right tabular-nums">{{ formatDuration(timing.summary.p90) }}</td>
                  <td class="py-1 text-right tabular-nums">{{ timing.summary.count }}</td>
                </tr>
              </tbody>
            </table>
          </div>
          <div class="text-wp-text-alt-100 mt-2 text-xs">{{ $t('repo.insights.group_runtime_desc') }}</div>
        </Panel>
      </div>
    </template>
  </div>
</template>

<script lang="ts" setup>
import { useDebounceFn } from '@vueuse/core';
import { computed, onMounted, ref, watch } from 'vue';
import { useI18n } from 'vue-i18n';
import { useRoute, useRouter } from 'vue-router';

import Icon from '~/components/atomic/Icon.vue';
import IconButton from '~/components/atomic/IconButton.vue';
import type { SelectOption } from '~/components/form/form.types';
import SelectField from '~/components/form/SelectField.vue';
import Panel from '~/components/layout/Panel.vue';
import InsightsChartPanel from '~/components/repo/insights/InsightsChartPanel.vue';
import InsightsHistoryGrid from '~/components/repo/insights/InsightsHistoryGrid.vue';
import {
  buildHistoryRows,
  chronological,
  formatBytes,
  formatDuration,
  formatPercent,
  groupTimings,
  kindTextClass,
  pipelineMemory,
  pipelineQueued,
  pipelineRuntime,
  statusKind,
  summarize,
} from '~/components/repo/insights/stats';
import PipelineStatusIcon from '~/components/repo/pipeline/PipelineStatusIcon.vue';
import useApiClient from '~/compositions/useApiClient';
import { useDate } from '~/compositions/useDate';
import { requiredInject } from '~/compositions/useInjectProvide';
import { useWPTitle } from '~/compositions/useWPTitle';
import type { RepoStats, StatsPipeline } from '~/lib/api/types';

const apiClient = useApiClient();
const route = useRoute();
const router = useRouter();
const { t } = useI18n();
const { toLocaleString, timeAgo } = useDate();

const repo = requiredInject('repo');
const repoPipelines = requiredInject('pipelines');

const ALL_BRANCHES = '*';
const DEFAULT_LIMIT = '50';
const limits = ['20', '50', '100'];

const tileClass =
  'border-wp-background-400 dark:border-wp-background-100 bg-wp-background-100 text-wp-text-100 flex min-w-0 flex-col gap-1 rounded-md border p-4';
const tileTitleClass = 'text-wp-text-alt-100 text-xs font-bold uppercase';

function firstLine(message: string) {
  return message.split('\n')[0];
}

// --- filters in the URL query ---

function queryValue(key: string): string | undefined {
  const v = route.query[key];
  return typeof v === 'string' && v !== '' ? v : undefined;
}

function setQuery(key: string, value: string | undefined) {
  void router.replace({ query: { ...route.query, [key]: value || undefined } });
}

const branch = computed({
  get: () => queryValue('branch') ?? repo.value.default_branch,
  set: (v: string) => setQuery('branch', v),
});

const event = computed({
  get: () => queryValue('event') ?? '',
  set: (v: string) => setQuery('event', v),
});

const limit = computed({
  get: () => {
    const v = queryValue('limit');
    return v !== undefined && limits.includes(v) ? v : DEFAULT_LIMIT;
  },
  set: (v: string) => setQuery('limit', v === DEFAULT_LIMIT ? undefined : v),
});

const branches = ref<string[]>([]);

async function loadBranches() {
  const all: string[] = [];
  // a handful of pages is plenty for a picker; the selected branch is always listed
  for (let page = 1; page <= 5; page++) {
    const batch = await apiClient.getRepoBranches(repo.value.id, { page });
    if (!batch || batch.length === 0) {
      break;
    }
    all.push(...batch);
  }
  branches.value = all;
}

const branchOptions = computed<SelectOption[]>(() => {
  const names = new Set([repo.value.default_branch, ...branches.value]);
  if (branch.value !== ALL_BRANCHES) {
    names.add(branch.value);
  }
  return [
    { value: ALL_BRANCHES, text: t('repo.insights.all_branches') },
    ...[...names].map((name) => ({
      value: name,
      text: name === repo.value.default_branch ? t('repo.insights.default_branch', { branch: name }) : name,
    })),
  ];
});

const eventOptions = computed<SelectOption[]>(() => [
  { value: '', text: t('repo.insights.all_events') },
  { value: 'push', text: t('repo.pipeline.event.push') },
  { value: 'pull_request', text: t('repo.pipeline.event.pr') },
  { value: 'tag', text: t('repo.pipeline.event.tag') },
  { value: 'release', text: t('repo.pipeline.event.release') },
  { value: 'deployment', text: t('repo.pipeline.event.deploy') },
  { value: 'cron', text: t('repo.pipeline.event.cron') },
  { value: 'promote', text: t('repo.pipeline.event.promote') },
  { value: 'manual', text: t('repo.pipeline.event.manual') },
]);

const limitOptions = computed<SelectOption[]>(() =>
  limits.map((l) => ({ value: l, text: t('repo.insights.last_pipelines', { count: Number(l) }) })),
);

// --- data ---

const stats = ref<RepoStats>();
const loading = ref(false);
const loadError = ref<string>();
const now = ref(Date.now() / 1000);
let requestSeq = 0;

async function load(silent = false) {
  const seq = ++requestSeq;
  loading.value = true;
  try {
    const result = await apiClient.getRepoStats(repo.value.id, {
      branch: branch.value,
      event: event.value || undefined,
      limit: Number(limit.value),
    });
    if (seq !== requestSeq) {
      return;
    }
    now.value = Date.now() / 1000;
    stats.value = result;
    loadError.value = undefined;
  } catch (error) {
    if (seq === requestSeq && !silent) {
      stats.value = undefined;
      loadError.value = error instanceof Error ? error.message : String(error);
    }
  } finally {
    if (seq === requestSeq) {
      loading.value = false;
    }
  }
}

watch([() => repo.value.id, branch, event, limit], async () => load());
watch(
  () => repo.value.id,
  async () => loadBranches(),
);
onMounted(async () => {
  await Promise.all([load(), loadBranches()]);
});

// reload quietly when a pipeline of this branch changes (the pipeline store gets server events)
const reloadSoon = useDebounceFn(async () => load(true), 3000);
watch(
  () =>
    repoPipelines.value
      .filter((p) => branch.value === ALL_BRANCHES || p.branch === branch.value)
      .filter((p) => !event.value || p.event === event.value)
      .map((p) => `${p.number}:${p.status}`)
      .join(','),
  (sig, old) => {
    if (sig !== old && stats.value) {
      void reloadSoon();
    }
  },
);

// --- derived views ---

const columns = computed(() => chronological(stats.value?.pipelines ?? []));
const latest = computed<StatsPipeline | undefined>(() => columns.value.at(-1));
const rows = computed(() => buildHistoryRows(columns.value));

const failing = computed(() =>
  rows.value
    .flatMap((r) => r.children)
    .filter((r) => r.failingStreak > 0)
    .sort((a, b) => b.failingStreak - a.failingStreak),
);

const branchPassRate = computed(() => {
  const kinds = columns.value.map((p) => statusKind(p.status));
  const passed = kinds.filter((k) => k === 'success').length;
  const total = passed + kinds.filter((k) => k === 'failure').length;
  return { passed, total, rate: total > 0 ? passed / total : undefined };
});

function series(value: (p: StatsPipeline) => number | undefined, pending?: (p: StatsPipeline) => boolean) {
  const points = columns.value.map((p) => ({
    number: p.number,
    value: value(p),
    message: firstLine(p.message),
    time: p.created > 0 ? toLocaleString(new Date(p.created * 1000)) : '',
    pending: pending?.(p) ?? false,
  }));
  return { points, summary: summarize(points.map((p) => p.value)) };
}

const runtime = computed(() => series(pipelineRuntime));
const queued = computed(() =>
  series(
    (p) => pipelineQueued(p, now.value),
    (p) => p.status === 'pending',
  ),
);
const memory = computed(() => series(pipelineMemory));
const groupRuntimes = computed(() => groupTimings(rows.value));

function groupLabel(group: string): string {
  switch (group) {
    case 'build':
      return t('repo.insights.groups.build');
    case 'test':
      return t('repo.insights.groups.test');
    case 'deploy':
      return t('repo.insights.groups.deploy');
    case 'other':
      return t('repo.insights.groups.other');
    default:
      return group.charAt(0).toUpperCase() + group.slice(1);
  }
}

async function openPipeline(pipelineNumber: number) {
  await router.push({ name: 'repo-pipeline-graph', params: { pipelineId: pipelineNumber } });
}

useWPTitle(computed(() => [t('repo.insights.title'), repo.value.full_name]));
</script>
