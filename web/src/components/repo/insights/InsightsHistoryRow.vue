<template>
  <div class="flex items-center" :class="{ 'border-wp-background-400 border-t pt-px': !child }">
    <component
      :is="child ? 'div' : 'button'"
      :type="child ? undefined : 'button'"
      class="bg-wp-background-100 text-wp-text-100 sticky left-0 z-[1] flex w-36 min-w-36 items-center gap-1 pr-2 text-left text-sm md:w-56 md:min-w-56"
      :class="{ 'pl-5': child, 'cursor-pointer': !child }"
      :aria-expanded="child ? undefined : expanded"
      @click="child || $emit('toggle')"
    >
      <Icon
        v-if="!child"
        name="chevron-right"
        class="h-4 w-4 min-w-4 transition-transform duration-150"
        :class="{ 'rotate-90': expanded }"
      />
      <div class="min-w-0">
        <div class="truncate" :class="{ 'font-bold': !child }" :title="label">{{ label }}</div>
        <div class="text-wp-text-alt-100 flex flex-wrap gap-x-2 text-xs">
          <span
            v-if="row.current"
            class="inline-block h-2 w-2 self-center rounded-full"
            :class="kindBgClass[row.current.kind]"
            :title="$t('repo.pipeline.status.status', { status: row.current.status })"
          />
          <span :title="$t('repo.insights.pass_rate_title', { passed: row.passed, failed: row.failed })">
            {{ formatPercent(row.passRate) }}
          </span>
          <span>{{ $t('repo.insights.runs', { count: row.runs }, row.runs) }}</span>
          <span v-if="row.failingStreak > 0" class="text-wp-error-100 font-bold">
            {{ $t('repo.insights.failing_for', { count: row.failingStreak }, row.failingStreak) }}
          </span>
        </div>
      </div>
    </component>

    <div class="flex gap-0.5 py-1">
      <template v-for="(cell, i) in row.cells" :key="cell ? cell.pipeline.id : `empty-${i}`">
        <router-link
          v-if="cell"
          :to="{ name: 'repo-pipeline-graph', params: { pipelineId: cell.pipeline.number } }"
          :title="cellTitle(cell)"
          class="hover:ring-wp-text-100 block h-4 w-4 rounded-sm hover:ring-2"
          :class="kindBgClass[cell.kind]"
        />
        <div v-else class="flex h-4 w-4 items-center justify-center" aria-hidden="true">
          <div class="bg-wp-background-400 h-1 w-1 rounded-full" />
        </div>
      </template>
    </div>
  </div>
</template>

<script lang="ts" setup>
import Icon from '~/components/atomic/Icon.vue';
import { useDate } from '~/compositions/useDate';

import { formatPercent, kindBgClass } from './stats';
import type { HistoryCell, HistoryRow } from './stats';

const props = defineProps<{
  row: HistoryRow;
  label: string;
  child?: boolean;
  expanded?: boolean;
}>();

defineEmits<{
  (event: 'toggle'): void;
}>();

const { toLocaleString } = useDate();

function cellTitle(cell: HistoryCell) {
  const p = cell.pipeline;
  return [
    `#${p.number} · ${props.label} · ${cell.status}`,
    p.message.split('\n')[0],
    p.created > 0 ? toLocaleString(new Date(p.created * 1000)) : '',
    '',
    ...cell.workflows.map((w) => `${w.name}: ${w.state}`),
  ].join('\n');
}
</script>
