<template>
  <Panel v-if="layout.lanes.length === 0">
    <span>{{ $t('repo.pipeline.graph.no_workflows') }}</span>
  </Panel>

  <div v-else class="w-full overflow-x-auto pb-2">
    <div ref="canvas" class="relative flex flex-col gap-4" :class="multiColumn ? 'w-max min-w-full' : 'w-full'">
      <GraphConnectors
        v-if="multiColumn"
        :container="canvas"
        :edges="edges"
        :highlight="hovered"
        :trigger="layoutSignature"
      />

      <section
        v-for="lane in layout.lanes"
        :key="lane.lane"
        class="border-wp-background-400 dark:border-wp-background-100 rounded-md border p-3"
      >
        <header class="text-wp-text-100 sticky left-3 z-10 mb-3 flex w-fit items-center gap-2">
          <PipelineStatusIcon :status="lane.status" class="h-5! w-5!" />
          <KindIcon :kind="lane.lane" size="md" />
          <h3 class="text-sm font-bold tracking-wide uppercase">{{ laneTitles[lane.lane] }}</h3>
          <span class="text-wp-text-alt-100 text-xs">
            {{ $t('repo.pipeline.graph.done', { done: lane.done, total: lane.total }) }}
          </span>
        </header>

        <div class="flex flex-col gap-3">
          <div v-for="cluster in lane.clusters" :key="cluster.key">
            <button
              v-if="cluster.subgroup !== ''"
              type="button"
              class="text-wp-text-100 hover:bg-wp-control-neutral-200 sticky left-3 z-10 mb-2 flex w-fit cursor-pointer items-center gap-1 rounded-md pr-2 text-sm"
              :aria-expanded="!isCollapsed(cluster)"
              :title="isCollapsed(cluster) ? $t('repo.pipeline.graph.expand') : $t('repo.pipeline.graph.collapse')"
              @click="toggle(cluster)"
            >
              <Icon
                name="chevron-right"
                class="transition-transform duration-150"
                :class="{ 'rotate-90': !isCollapsed(cluster) }"
              />
              <PipelineStatusIcon :status="cluster.status" class="h-4! w-4!" />
              <span class="font-medium">{{ cluster.subgroup }}</span>
              <span class="text-wp-text-alt-100 text-xs">
                {{ $t('repo.pipeline.graph.done', { done: cluster.done, total: cluster.nodes.length }) }}
              </span>
            </button>

            <div class="grid gap-y-2" :style="gridStyle">
              <ClusterCard
                v-if="isCollapsed(cluster)"
                :cluster="cluster"
                :style="multiColumn ? { gridColumn: `${cluster.minDepth + 1} / ${cluster.maxDepth + 2}` } : undefined"
                @expand="toggle(cluster)"
                @mouseenter="hovered = clusterAnchor(cluster.key)"
                @mouseleave="hovered = undefined"
              />
              <template v-else-if="multiColumn">
                <div
                  v-for="column in layout.columns"
                  :key="column"
                  class="flex flex-col gap-2"
                  :style="{ gridColumn: column }"
                >
                  <WorkflowCard
                    v-for="node in nodesAt(cluster, column - 1)"
                    :key="node.workflow.id"
                    :workflow="node.workflow"
                    :workflows="workflows"
                    @mouseenter="hovered = workflowAnchor(node.workflow.id)"
                    @mouseleave="hovered = undefined"
                  />
                </div>
              </template>
              <template v-else>
                <WorkflowCard
                  v-for="node in cluster.nodes"
                  :key="node.workflow.id"
                  :workflow="node.workflow"
                  :workflows="workflows"
                  class="self-start"
                />
              </template>
            </div>
          </div>
        </div>
      </section>
    </div>
  </div>
</template>

<script lang="ts" setup>
import { computed, reactive, ref, useTemplateRef, watch } from 'vue';
import { useI18n } from 'vue-i18n';

import Icon from '~/components/atomic/Icon.vue';
import Panel from '~/components/layout/Panel.vue';
import KindIcon from '~/components/repo/pipeline/kind/KindIcon.vue';
import PipelineStatusIcon from '~/components/repo/pipeline/PipelineStatusIcon.vue';
import type { Pipeline } from '~/lib/api/types';

import ClusterCard from './ClusterCard.vue';
import GraphConnectors from './GraphConnectors.vue';
import { buildGraph, clusterAnchor, defaultCollapsed, resolveEdges, workflowAnchor } from './layout';
import type { GraphCluster, GraphNode, Lane } from './layout';
import WorkflowCard from './WorkflowCard.vue';

const props = defineProps<{
  pipeline: Pipeline;
}>();

const { t } = useI18n();

const canvas = useTemplateRef<HTMLElement>('canvas');
const hovered = ref<string>();

const workflows = computed(() => props.pipeline.workflows ?? []);
const layout = computed(() => buildGraph(workflows.value));
const multiColumn = computed(() => layout.value.columns > 1);

const laneTitles = computed<Record<Lane, string>>(() => ({
  build: t('repo.pipeline.graph.lanes.build'),
  test: t('repo.pipeline.graph.lanes.test'),
  deploy: t('repo.pipeline.graph.lanes.deploy'),
  other: t('repo.pipeline.graph.lanes.other'),
}));

// clusters the user opened or closed; the others follow defaultCollapsed as their workflows progress
const collapseOverrides = reactive(new Map<string, boolean>());
watch(
  () => props.pipeline.id,
  () => collapseOverrides.clear(),
);

function isCollapsed(cluster: GraphCluster): boolean {
  return collapseOverrides.get(cluster.key) ?? defaultCollapsed(cluster);
}

function toggle(cluster: GraphCluster) {
  collapseOverrides.set(cluster.key, !isCollapsed(cluster));
  hovered.value = undefined;
}

const collapsedKeys = computed(
  () =>
    new Set(
      layout.value.lanes
        .flatMap((lane) => lane.clusters)
        .filter(isCollapsed)
        .map((cluster) => cluster.key),
    ),
);

const edges = computed(() => resolveEdges(layout.value, collapsedKeys.value));

// changes whenever cards are added, moved, collapsed or change state, so the connectors are measured again
const layoutSignature = computed(() =>
  [
    layout.value.columns,
    ...collapsedKeys.value,
    ...workflows.value.map((workflow) => `${workflow.id}:${workflow.state}:${workflow.children?.length ?? 0}`),
  ].join('|'),
);

// fixed column widths keep the columns of all lanes and clusters aligned
const gridStyle = computed(() =>
  multiColumn.value
    ? { gridTemplateColumns: `repeat(${layout.value.columns}, 18rem)`, columnGap: '3.5rem' }
    : { gridTemplateColumns: 'repeat(auto-fill, minmax(15rem, 1fr))', columnGap: '0.75rem' },
);

function nodesAt(cluster: GraphCluster, depth: number): GraphNode[] {
  return cluster.nodes.filter((node) => node.depth === depth);
}
</script>
