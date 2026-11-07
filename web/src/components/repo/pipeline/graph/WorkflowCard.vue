<template>
  <div
    :data-graph-anchor="workflowAnchor(workflow.id)"
    class="bg-wp-background-200 hover:bg-wp-control-neutral-100 dark:hover:bg-wp-control-neutral-200 relative z-10 w-full overflow-hidden rounded-md border shadow-sm"
    :class="statusBorder(workflow.state)"
  >
    <div class="flex items-center gap-2 px-3 pt-2">
      <PipelineStatusIcon :status="workflow.state" class="h-5! w-5! shrink-0" />
      <router-link
        :to="{ name: 'repo-pipeline', params: { stepId: workflow.pid } }"
        :title="workflow.name"
        class="text-wp-text-100 min-w-0 grow truncate font-medium after:absolute after:inset-0"
      >
        {{ workflow.name }}
      </router-link>
      <button
        v-if="steps.length > 0"
        type="button"
        class="text-wp-text-alt-100 hover:bg-wp-control-neutral-200 relative z-10 -mr-1 shrink-0 cursor-pointer rounded-md"
        :title="expanded ? $t('repo.pipeline.graph.hide_steps') : $t('repo.pipeline.graph.show_steps')"
        :aria-expanded="expanded"
        @click="expanded = !expanded"
      >
        <Icon name="chevron-right" class="transition-transform duration-150" :class="{ 'rotate-90': expanded }" />
      </button>
    </div>

    <div class="flex min-h-6 items-center gap-2 px-3 pt-0.5 pb-2 text-xs">
      <span
        v-if="detail"
        class="min-w-0 truncate"
        :class="detail.tone === 'error' ? 'text-wp-error-100' : 'text-wp-text-alt-100'"
        :title="detail.title"
      >
        {{ detail.text }}
      </span>
      <PipelineStepDuration v-if="workflow.started" :workflow="workflow" class="text-wp-text-alt-100 shrink-0" />
    </div>

    <GraphStatusBar :states="steps.map((step) => step.state)" />

    <ul v-if="expanded" class="border-wp-background-400 dark:border-wp-background-100 relative z-10 border-t py-1">
      <li v-for="step in steps" :key="step.id">
        <router-link
          :to="{ name: 'repo-pipeline', params: { stepId: step.pid } }"
          :title="step.name"
          class="hover:bg-wp-control-neutral-200 text-wp-text-100 flex items-center gap-2 px-3 py-1 text-sm"
        >
          <PipelineStatusIcon
            :service="step.type === StepType.Service"
            :status="step.state"
            class="h-4! w-4! shrink-0"
          />
          <span class="min-w-0 grow truncate">{{ step.name }}</span>
          <span v-if="stepNote(step)" class="text-wp-text-alt-100 max-w-1/2 shrink-0 truncate text-xs">
            {{ stepNote(step) }}
          </span>
          <PipelineStepDuration v-else-if="step.started" :step="step" class="text-wp-text-alt-100 shrink-0" />
        </router-link>
      </li>
    </ul>
  </div>
</template>

<script lang="ts" setup>
import { computed, ref } from 'vue';
import { useI18n } from 'vue-i18n';

import Icon from '~/components/atomic/Icon.vue';
import PipelineStatusIcon from '~/components/repo/pipeline/PipelineStatusIcon.vue';
import PipelineStepDuration from '~/components/repo/pipeline/PipelineStepDuration.vue';
import { StepType } from '~/lib/api/types';
import type { PipelineStep, PipelineWorkflow } from '~/lib/api/types';

import GraphStatusBar from './GraphStatusBar.vue';
import { isRunning, stepGate, stepProgress, workflowAnchor, workflowGate } from './layout';
import { statusBorder, useGateText } from './useGateText';

const props = defineProps<{
  workflow: PipelineWorkflow;
  // all workflows of the pipeline, to explain what this one waits for
  workflows: PipelineWorkflow[];
}>();

const { t } = useI18n();
const { gateText, stepGateText } = useGateText();

const expanded = ref(false);
const steps = computed(() => props.workflow.children ?? []);

const detail = computed<{ text: string; title: string; tone: 'error' | 'muted' } | undefined>(() => {
  const workflow = props.workflow;
  const gate = workflowGate(workflow, props.workflows);
  if (gate) {
    return {
      text: gateText(gate),
      title: gateText(gate, false),
      tone: gate.kind === 'skipped' && gate.reason === 'failed' ? 'error' : 'muted',
    };
  }
  if (workflow.error) {
    return { text: workflow.error, title: workflow.error, tone: 'error' };
  }
  const { done, total } = stepProgress(workflow);
  if (total > 0 && (isRunning(workflow.state) || done < total)) {
    const text = t('repo.pipeline.graph.steps', { done, total });
    return { text, title: text, tone: 'muted' };
  }
  return undefined;
});

function stepNote(step: PipelineStep): string | undefined {
  const gate = stepGate(step, steps.value);
  return gate ? stepGateText(gate) : undefined;
}
</script>
