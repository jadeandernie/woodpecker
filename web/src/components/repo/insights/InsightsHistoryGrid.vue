<template>
  <div class="flex flex-col gap-2">
    <div class="flex flex-wrap items-center gap-2">
      <span class="text-wp-text-alt-100 text-sm">{{ $t('repo.insights.history_desc') }}</span>
      <div class="ml-auto flex gap-1">
        <IconButton icon="expand-all" class="h-8 w-8" :title="$t('repo.insights.expand_all')" @click="expandAll" />
        <IconButton
          icon="collapse-all"
          class="h-8 w-8"
          :title="$t('repo.insights.collapse_all')"
          @click="expanded = new Set()"
        />
      </div>
    </div>

    <div ref="scroller" class="overflow-x-auto pb-1">
      <div class="inline-flex min-w-full flex-col">
        <!-- the branch's pipelines: one square per pipeline, newest right, aligned with the rows below -->
        <div class="flex items-center">
          <div
            class="bg-wp-background-100 text-wp-text-100 sticky left-0 z-[1] w-36 min-w-36 truncate pr-2 text-sm font-bold md:w-56 md:min-w-56"
          >
            {{ $t('repo.insights.pipelines_row') }}
          </div>
          <div class="flex gap-0.5 py-1">
            <router-link
              v-for="column in columns"
              :key="column.id"
              :to="{ name: 'repo-pipeline', params: { pipelineId: column.number } }"
              :title="pipelineTitle(column)"
              class="hover:ring-wp-text-100 block h-4 w-4 rounded-sm hover:ring-2"
              :class="kindBgClass[statusKind(column.status)]"
            />
          </div>
        </div>

        <template v-for="row in rows" :key="row.key">
          <InsightsHistoryRow
            :row="row"
            :label="groupLabel(row.group)"
            :expanded="expanded.has(row.key)"
            @toggle="toggle(row.key)"
          />
          <template v-if="expanded.has(row.key)">
            <InsightsHistoryRow
              v-for="child in row.children"
              :key="child.key"
              :row="child"
              :label="child.subgroup || $t('repo.insights.no_subgroup')"
              child
            />
          </template>
        </template>
      </div>
    </div>
  </div>
</template>

<script lang="ts" setup>
import { nextTick, ref, useTemplateRef, watch } from 'vue';
import { useI18n } from 'vue-i18n';

import IconButton from '~/components/atomic/IconButton.vue';
import { useDate } from '~/compositions/useDate';
import type { StatsPipeline } from '~/lib/api/types';

import InsightsHistoryRow from './InsightsHistoryRow.vue';
import { kindBgClass, statusKind } from './stats';
import type { HistoryRow } from './stats';

const props = defineProps<{
  columns: StatsPipeline[]; // oldest first
  rows: HistoryRow[];
  groupLabel: (group: string) => string;
}>();

const { t } = useI18n();
const { toLocaleString } = useDate();

const expanded = ref(new Set<string>());

function toggle(key: string) {
  const next = new Set(expanded.value);
  if (!next.delete(key)) {
    next.add(key);
  }
  expanded.value = next;
}

function expandAll() {
  expanded.value = new Set(props.rows.map((r) => r.key));
}

function pipelineTitle(p: StatsPipeline) {
  return [
    `#${p.number} · ${t('repo.pipeline.status.status', { status: p.status })}`,
    p.message.split('\n')[0],
    p.created > 0 ? toLocaleString(new Date(p.created * 1000)) : '',
  ].join('\n');
}

// open the groups holding a failing subgroup on the first load, keep what the user opened afterwards
watch(
  () => props.rows,
  (rows, old) => {
    if (old === undefined || old.length === 0) {
      expanded.value = new Set(rows.filter((r) => r.children.some((c) => c.failingStreak > 0)).map((r) => r.key));
    }
  },
  { immediate: true },
);

// newest pipelines are on the right: start scrolled to the end
const scroller = useTemplateRef<HTMLElement>('scroller');
watch(
  () => props.columns,
  async () => {
    await nextTick();
    if (scroller.value) {
      scroller.value.scrollLeft = scroller.value.scrollWidth;
    }
  },
  { immediate: true },
);
</script>
