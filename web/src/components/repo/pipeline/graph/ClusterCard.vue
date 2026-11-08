<template>
  <button
    type="button"
    :data-graph-anchor="clusterAnchor(cluster.key)"
    class="bg-wp-background-200 hover:bg-wp-control-neutral-100 dark:hover:bg-wp-control-neutral-200 relative z-10 w-full cursor-pointer overflow-hidden rounded-md border border-dashed text-left shadow-sm"
    :class="statusBorder(cluster.status)"
    :title="$t('repo.pipeline.graph.expand')"
    @click="$emit('expand')"
  >
    <div class="flex items-center gap-2 px-3 pt-2">
      <PipelineStatusIcon :status="cluster.status" class="h-5! w-5! shrink-0" />
      <span class="text-wp-text-100 min-w-0 grow truncate font-medium">{{ cluster.subgroup }}</span>
      <KindIcon :kind="cluster.lane" :subgroup="cluster.subgroup" />
      <CountBadge :value="cluster.nodes.length" />
    </div>
    <div class="text-wp-text-alt-100 flex min-h-6 items-center justify-between gap-2 px-3 pt-0.5 pb-2 text-xs">
      <span class="truncate">{{ $t('repo.pipeline.graph.workflows', { count: cluster.nodes.length }) }}</span>
      <span class="shrink-0">{{
        $t('repo.pipeline.graph.done', { done: cluster.done, total: cluster.nodes.length })
      }}</span>
    </div>
    <GraphStatusBar :states="cluster.nodes.map((node) => node.workflow.state)" />
  </button>
</template>

<script lang="ts" setup>
import CountBadge from '~/components/atomic/CountBadge.vue';
import KindIcon from '~/components/repo/pipeline/kind/KindIcon.vue';
import PipelineStatusIcon from '~/components/repo/pipeline/PipelineStatusIcon.vue';

import GraphStatusBar from './GraphStatusBar.vue';
import { clusterAnchor } from './layout';
import type { GraphCluster } from './layout';
import { statusBorder } from './useGateText';

defineProps<{
  cluster: GraphCluster;
}>();

defineEmits<{
  (event: 'expand'): void;
}>();
</script>
