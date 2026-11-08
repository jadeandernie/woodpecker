// The kind of a workflow (build, test, deploy or other) and how the web UI shows it. A workflow's kind is the one its
// config declares (`kind:`), else the group the server derived from its name and the repo's workflow group rules. A
// pipeline's kinds are the distinct kinds of its workflows.

import { useI18n } from 'vue-i18n';

import type { IconNames } from '~/components/atomic/Icon.vue';
import type { Pipeline, PipelineWorkflow } from '~/lib/api/types';

// in display order
export const PIPELINE_KINDS = ['build', 'test', 'deploy', 'other'] as const;
export type PipelineKind = (typeof PIPELINE_KINDS)[number];

export interface PipelineKindStyle {
  icon: IconNames;
  // text colour classes for the icon, readable on the light and the dark theme; the hues stay clear of the status
  // colours (green, red, blue, yellow) so a kind is never read as a state
  color: string;
}

export const pipelineKindStyles: Record<PipelineKind, PipelineKindStyle> = {
  build: { icon: 'kind-build', color: 'text-orange-600 dark:text-orange-400' },
  test: { icon: 'kind-test', color: 'text-violet-600 dark:text-violet-400' },
  deploy: { icon: 'kind-deploy', color: 'text-teal-600 dark:text-teal-400' },
  other: { icon: 'kind-other', color: 'text-wp-text-alt-100' },
};

// toPipelineKind maps a declared kind or a group to a kind; groups from repo rules other than build, test and deploy
// are 'other'
export function toPipelineKind(value: string | null | undefined): PipelineKind {
  const kind = value?.trim().toLowerCase() ?? '';
  return (PIPELINE_KINDS as readonly string[]).includes(kind) ? (kind as PipelineKind) : 'other';
}

export function workflowKind(workflow: Pick<PipelineWorkflow, 'kind' | 'group'>): PipelineKind {
  return toPipelineKind(workflow.kind || workflow.group);
}

function ordered(kinds: Iterable<PipelineKind>): PipelineKind[] {
  const set = new Set(kinds);
  return PIPELINE_KINDS.filter((kind) => set.has(kind));
}

// pipelineKinds returns the distinct kinds of a pipeline's workflows in display order: the `kinds` the pipeline list
// sends, or, when a pipeline comes without them (as live updates do), those of its workflows
export function pipelineKinds(pipeline: Pick<Pipeline, 'kinds' | 'workflows'>): PipelineKind[] {
  if (pipeline.kinds && pipeline.kinds.length > 0) {
    return ordered(pipeline.kinds.map(toPipelineKind));
  }
  return ordered((pipeline.workflows ?? []).map(workflowKind));
}

// the kind that says most about what a pipeline does: one that deploys is a deploy pipeline even if it builds and
// tests first, one that tests is a test pipeline even if it builds first
const primaryOrder: PipelineKind[] = ['deploy', 'test', 'build', 'other'];

export function primaryPipelineKind(pipeline: Pick<Pipeline, 'kinds' | 'workflows'>): PipelineKind | undefined {
  const kinds = pipelineKinds(pipeline);
  return primaryOrder.find((kind) => kinds.includes(kind));
}

export function usePipelineKindLabel() {
  const { t } = useI18n();
  return (kind: PipelineKind): string => {
    switch (kind) {
      case 'build':
        return t('repo.pipeline.kind.build');
      case 'test':
        return t('repo.pipeline.kind.test');
      case 'deploy':
        return t('repo.pipeline.kind.deploy');
      case 'other':
        return t('repo.pipeline.kind.other');
    }
  };
}
