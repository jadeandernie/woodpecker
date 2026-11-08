<template>
  <span v-if="kinds.length > 0" class="inline-flex shrink-0 items-center" :class="label ? 'gap-3' : 'gap-1.5'">
    <KindIcon v-for="kind in kinds" :key="kind" :kind="kind" :label="label" />
  </span>
</template>

<script lang="ts" setup>
import { computed } from 'vue';

import type { Pipeline } from '~/lib/api/types';
import { pipelineKinds } from '~/lib/pipeline-kind';

import KindIcon from './KindIcon.vue';

const props = defineProps<{
  pipeline: Pick<Pipeline, 'kinds' | 'workflows'>;
  // show each kind's name next to its icon
  label?: boolean;
}>();

const kinds = computed(() => pipelineKinds(props.pipeline));
</script>
