<template>
  <div ref="container" class="relative w-full" @mouseleave="hovered = undefined">
    <svg
      v-if="width > 0"
      :width="width"
      :height="height"
      :viewBox="`0 0 ${width} ${height}`"
      class="block overflow-visible"
      role="img"
      :aria-label="label"
    >
      <!-- y grid and tick labels -->
      <g v-for="tick in ticks" :key="tick">
        <line
          :x1="margin.left"
          :x2="width - margin.right"
          :y1="y(tick)"
          :y2="y(tick)"
          class="stroke-wp-background-400"
          stroke-width="1"
          :stroke-dasharray="tick === 0 ? undefined : '2 3'"
        />
        <text
          :x="margin.left - 6"
          :y="y(tick)"
          text-anchor="end"
          dominant-baseline="middle"
          class="fill-wp-text-alt-100 text-[10px]"
        >
          {{ format(tick) }}
        </text>
      </g>

      <!-- bars -->
      <g v-for="(point, i) in points" :key="point.number">
        <rect
          v-if="point.value !== undefined"
          :x="bandX(i) + barGap / 2"
          :y="y(point.value)"
          :width="barWidth"
          :height="Math.max(1, y(0) - y(point.value))"
          rx="1"
          :class="[
            point.pending ? 'fill-wp-state-warn-100' : 'fill-wp-link-100',
            hovered !== undefined && hovered !== i ? 'opacity-50' : '',
          ]"
        />
        <!-- the gap of a pipeline without a value: a small mark on the baseline -->
        <line
          v-else
          :x1="bandX(i) + band / 2 - 1.5"
          :x2="bandX(i) + band / 2 + 1.5"
          :y1="y(0) + 3"
          :y2="y(0) + 3"
          class="stroke-wp-text-alt-100"
          stroke-width="1"
        />
      </g>

      <!-- median -->
      <g v-if="median !== undefined">
        <line
          :x1="margin.left"
          :x2="width - margin.right"
          :y1="y(median)"
          :y2="y(median)"
          class="stroke-wp-text-100"
          stroke-width="1"
          stroke-dasharray="4 3"
        />
        <text :x="width - margin.right" :y="y(median) - 3" text-anchor="end" class="fill-wp-text-100 text-[10px]">
          {{ $t('repo.insights.median') }}
        </text>
      </g>

      <!-- x labels: a few pipeline numbers -->
      <text
        v-for="i in xLabelIndexes"
        :key="`x${i}`"
        :x="bandX(i) + band / 2"
        :y="height - 4"
        text-anchor="middle"
        class="fill-wp-text-alt-100 text-[10px]"
      >
        {{ `#${points[i].number}` }}
      </text>

      <!-- hit areas, wider than the bars -->
      <rect
        v-for="(point, i) in points"
        :key="`hit${point.number}`"
        :x="bandX(i)"
        :y="margin.top"
        :width="band"
        :height="y(0) - margin.top"
        fill="transparent"
        class="cursor-pointer"
        @mouseenter="hovered = i"
        @click="$emit('select', point.number)"
      />
    </svg>

    <div
      v-if="hoveredPoint && hovered !== undefined"
      class="border-wp-background-400 bg-wp-background-100 text-wp-text-100 pointer-events-none absolute z-10 w-56 rounded-md border px-2 py-1 text-xs shadow-md"
      :style="tooltipStyle"
    >
      <div class="flex justify-between gap-2 font-bold">
        <span>{{ `#${hoveredPoint.number}` }}</span>
        <span>{{
          hoveredPoint.value === undefined ? $t('repo.insights.not_measured') : format(hoveredPoint.value)
        }}</span>
      </div>
      <div class="truncate">{{ hoveredPoint.message }}</div>
      <div class="text-wp-text-alt-100">{{ hoveredPoint.time }}</div>
      <div v-if="hoveredPoint.pending" class="text-wp-state-warn-100">{{ $t('repo.insights.still_waiting') }}</div>
    </div>
  </div>
</template>

<script lang="ts" setup>
import { useElementSize } from '@vueuse/core';
import { computed, ref, useTemplateRef } from 'vue';

import { niceTicks } from './stats';
import type { AxisUnit } from './stats';

export interface BarPoint {
  number: number; // pipeline number
  value?: number; // undefined = not measured, drawn as a gap
  message: string;
  time: string;
  pending?: boolean; // still waiting, the value grows
}

const props = withDefaults(
  defineProps<{
    points: BarPoint[]; // oldest first
    unit: AxisUnit;
    format: (value: number) => string;
    label: string;
    median?: number;
    height?: number;
  }>(),
  { median: undefined, height: 160 },
);

defineEmits<{
  (event: 'select', pipelineNumber: number): void;
}>();

const container = useTemplateRef<HTMLElement>('container');
const { width } = useElementSize(container);

const margin = { top: 8, right: 4, bottom: 18, left: 52 };
const hovered = ref<number>();

const maxValue = computed(() => Math.max(0, ...props.points.map((p) => p.value ?? 0), props.median ?? 0));
const ticks = computed(() => niceTicks(maxValue.value, props.unit));
const top = computed(() => ticks.value.at(-1) || 1);

const plotWidth = computed(() => Math.max(1, width.value - margin.left - margin.right));
const band = computed(() => plotWidth.value / Math.max(1, props.points.length));
const barGap = computed(() => (band.value > 6 ? 2 : band.value > 3 ? 1 : 0));
const barWidth = computed(() => Math.max(1, band.value - barGap.value));

function bandX(i: number) {
  return margin.left + i * band.value;
}

function y(value: number) {
  const plotHeight = props.height - margin.top - margin.bottom;
  return margin.top + plotHeight * (1 - Math.min(value, top.value) / top.value);
}

// about one label per 70px, always the newest
const xLabelIndexes = computed(() => {
  const n = props.points.length;
  if (n === 0) {
    return [];
  }
  const every = Math.max(1, Math.ceil(n / Math.max(1, Math.floor(plotWidth.value / 70))));
  const indexes: number[] = [];
  for (let i = n - 1; i >= 0; i -= every) {
    indexes.push(i);
  }
  return indexes;
});

const hoveredPoint = computed(() => (hovered.value === undefined ? undefined : props.points[hovered.value]));

const tooltipStyle = computed(() => {
  const x = bandX(hovered.value ?? 0) + band.value / 2;
  // keep the 14rem tooltip inside the chart
  const left = Math.min(Math.max(0, x - 112), Math.max(0, width.value - 224));
  return { left: `${left}px`, top: `${props.height}px` };
});
</script>
