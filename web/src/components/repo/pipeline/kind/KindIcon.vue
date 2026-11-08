<template>
  <span
    class="inline-flex shrink-0 items-center gap-1"
    :class="style.color"
    :title="title"
    :role="label ? undefined : 'img'"
    :aria-label="label ? undefined : title"
  >
    <Icon :name="style.icon" :class="size === 'md' ? 'h-5! w-5!' : 'h-4! w-4!'" />
    <span v-if="label" class="text-xs font-medium">{{ kindLabel(kind) }}</span>
  </span>
</template>

<script lang="ts" setup>
import { computed } from 'vue';
import { useI18n } from 'vue-i18n';

import Icon from '~/components/atomic/Icon.vue';
import { pipelineKindStyles, usePipelineKindLabel } from '~/lib/pipeline-kind';
import type { PipelineKind } from '~/lib/pipeline-kind';

const props = defineProps<{
  kind: PipelineKind;
  // the subject of a workflow (knowledge, tensor, ...), named in the tooltip
  subgroup?: string;
  // show the kind's name next to the icon
  label?: boolean;
  size?: 'sm' | 'md';
}>();

const { t } = useI18n();
const kindLabel = usePipelineKindLabel();

const style = computed(() => pipelineKindStyles[props.kind]);
const title = computed(() =>
  props.subgroup
    ? t('repo.pipeline.kind.title_subgroup', { kind: kindLabel(props.kind), subgroup: props.subgroup })
    : t('repo.pipeline.kind.title', { kind: kindLabel(props.kind) }),
);
</script>
