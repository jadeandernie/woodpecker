<template>
  <svg
    class="pointer-events-none absolute top-0 left-0 z-[5] overflow-visible"
    :width="size.width"
    :height="size.height"
    aria-hidden="true"
  >
    <g v-for="path in paths" :key="path.key" :class="[strokeClass(path.status), { 'opacity-25': isDimmed(path) }]">
      <path
        :d="path.d"
        fill="none"
        :stroke-width="isHighlighted(path) ? 2.5 : 1.5"
        :stroke-dasharray="dashed(path.status) ? '4 4' : undefined"
        :class="{ 'graph-connector-flow': isRunning(path.status) }"
      />
      <circle :cx="path.x2" :cy="path.y2" r="3" class="fill-current" />
    </g>
  </svg>
</template>

<script lang="ts" setup>
import { nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue';

import { pipelineStatusColors } from '~/components/repo/pipeline/pipeline-status';
import type { PipelineStatus } from '~/lib/api/types';

import { isRunning } from './layout';
import type { ResolvedEdge } from './layout';

const props = defineProps<{
  // the element that holds the cards; paths are drawn in its coordinates
  container: HTMLElement | null;
  edges: ResolvedEdge[];
  // the anchor that is hovered, its connectors are highlighted
  highlight?: string;
  // anything that changes where cards are; the connectors are measured again when it changes
  trigger?: unknown;
}>();

interface DrawnPath {
  key: string;
  from: string;
  to: string;
  status: PipelineStatus;
  d: string;
  x2: number;
  y2: number;
}

const paths = ref<DrawnPath[]>([]);
const size = ref({ width: 0, height: 0 });

// connectors attach to the header row of a card, not its middle, so expanded cards keep their lines in place
const HEADER_HALF_HEIGHT = 20;

function measure() {
  const root = props.container;
  if (!root) {
    paths.value = [];
    return;
  }

  const origin = root.getBoundingClientRect();
  size.value = { width: root.scrollWidth, height: root.scrollHeight };

  const anchors = new Map<string, DOMRect>();
  root.querySelectorAll<HTMLElement>('[data-graph-anchor]').forEach((element) => {
    anchors.set(element.dataset.graphAnchor!, element.getBoundingClientRect());
  });

  const drawn: DrawnPath[] = [];
  for (const edge of props.edges) {
    const from = anchors.get(edge.from);
    const to = anchors.get(edge.to);
    if (!from || !to) {
      continue;
    }
    const x1 = from.right - origin.left;
    const y1 = from.top - origin.top + Math.min(from.height / 2, HEADER_HALF_HEIGHT);
    const x2 = to.left - origin.left;
    const y2 = to.top - origin.top + Math.min(to.height / 2, HEADER_HALF_HEIGHT);
    // a smooth curve to the right; when the target is not right of the source (collapsed clusters can overlap
    // columns) the curve loops around
    const bend = x2 > x1 ? Math.max(16, (x2 - x1) / 2) : 48;
    drawn.push({
      ...edge,
      d: `M ${x1} ${y1} C ${x1 + bend} ${y1}, ${x2 - bend} ${y2}, ${x2} ${y2}`,
      x2,
      y2,
    });
  }
  paths.value = drawn;
  observeAnchors(root);
}

let frame: number | undefined;
function scheduleMeasure() {
  if (frame !== undefined) {
    return;
  }
  frame = requestAnimationFrame(() => {
    frame = undefined;
    measure();
  });
}

// cards change size when they expand or their text changes, so watch every card besides the container
const observer = typeof ResizeObserver === 'undefined' ? undefined : new ResizeObserver(scheduleMeasure);
const observed = new Set<Element>();
function observeAnchors(root: HTMLElement) {
  if (!observer) {
    return;
  }
  const current = new Set<Element>([root, ...root.querySelectorAll('[data-graph-anchor]')]);
  for (const element of observed) {
    if (!current.has(element)) {
      observer.unobserve(element);
      observed.delete(element);
    }
  }
  for (const element of current) {
    if (!observed.has(element)) {
      observer.observe(element);
      observed.add(element);
    }
  }
}

watch(
  () => [props.container, props.edges, props.trigger],
  async () => {
    await nextTick();
    scheduleMeasure();
  },
  { flush: 'post' },
);

onMounted(() => {
  scheduleMeasure();
  window.addEventListener('resize', scheduleMeasure);
});

onBeforeUnmount(() => {
  observer?.disconnect();
  window.removeEventListener('resize', scheduleMeasure);
  if (frame !== undefined) {
    cancelAnimationFrame(frame);
  }
});

const strokes = {
  red: 'text-wp-error-100 stroke-wp-error-100',
  green: 'text-wp-state-ok-100 stroke-wp-state-ok-100',
  blue: 'text-wp-state-info-100 stroke-wp-state-info-100',
  orange: 'text-wp-state-neutral-100 stroke-wp-state-neutral-100',
  gray: 'text-wp-state-neutral-100 stroke-wp-state-neutral-100',
} as const;

function strokeClass(status: PipelineStatus): string {
  return strokes[pipelineStatusColors[status]];
}

// a connector whose source has not finished is a gate that is still closed
function dashed(status: PipelineStatus): boolean {
  return status === 'pending' || status === 'blocked' || isRunning(status);
}

function isHighlighted(path: DrawnPath): boolean {
  return props.highlight !== undefined && (path.from === props.highlight || path.to === props.highlight);
}

function isDimmed(path: DrawnPath): boolean {
  return props.highlight !== undefined && !isHighlighted(path);
}
</script>

<style scoped>
.graph-connector-flow {
  animation: graph-connector-flow 0.8s linear infinite;
}

@keyframes graph-connector-flow {
  to {
    stroke-dashoffset: -8;
  }
}
</style>
