<template>
  <Panel :title="title">
    <dl class="mb-3 grid grid-cols-3 gap-2">
      <div class="min-w-0">
        <dt class="text-wp-text-alt-100 text-xs">{{ $t('repo.insights.latest') }}</dt>
        <dd class="text-wp-text-200 truncate text-xl font-bold tabular-nums">{{ show(summary.latest) }}</dd>
      </div>
      <div class="min-w-0">
        <dt class="text-wp-text-alt-100 text-xs">{{ $t('repo.insights.median') }}</dt>
        <dd class="text-wp-text-200 truncate text-xl tabular-nums">{{ show(summary.median) }}</dd>
      </div>
      <div class="min-w-0">
        <dt class="text-wp-text-alt-100 text-xs">{{ $t('repo.insights.p90') }}</dt>
        <dd class="text-wp-text-200 truncate text-xl tabular-nums">{{ show(summary.p90) }}</dd>
      </div>
    </dl>
    <InsightsBarChart
      :points="points"
      :unit="unit"
      :format="format"
      :label="title"
      :median="summary.median"
      @select="$emit('select', $event)"
    />
    <div v-if="$slots.default" class="text-wp-text-alt-100 mt-2 text-xs">
      <slot />
    </div>
  </Panel>
</template>

<script lang="ts" setup>
import Panel from '~/components/layout/Panel.vue';

import InsightsBarChart from './InsightsBarChart.vue';
import type { BarPoint } from './InsightsBarChart.vue';
import type { AxisUnit, Summary } from './stats';

const props = defineProps<{
  title: string;
  summary: Summary;
  points: BarPoint[];
  unit: AxisUnit;
  format: (value: number) => string;
}>();

defineEmits<{
  (event: 'select', pipelineNumber: number): void;
}>();

function show(value: number | undefined) {
  return value === undefined ? '–' : props.format(value);
}
</script>
