import { useI18n } from 'vue-i18n';

import { pipelineStatusColors } from '~/components/repo/pipeline/pipeline-status';
import type { PipelineStatus } from '~/lib/api/types';

import type { Gate } from './layout';

// how many dependency names a card shows before it abbreviates the rest
const SHOWN_NAMES = 2;

export function useGateText() {
  const { t } = useI18n();

  function names(list: string[], short: boolean): string {
    if (!short || list.length <= SHOWN_NAMES) {
      return list.join(', ');
    }
    return t('repo.pipeline.graph.more', {
      names: list.slice(0, SHOWN_NAMES).join(', '),
      count: list.length - SHOWN_NAMES,
    });
  }

  // gateText describes a gate, short abbreviates long dependency lists
  function gateText(gate: Gate, short = true): string {
    switch (gate.kind) {
      case 'waiting':
        return t('repo.pipeline.graph.waiting_for', { names: names(gate.on, short) });
      case 'blocked':
        return t('repo.pipeline.graph.awaiting_approval');
      case 'queued':
        return t('repo.pipeline.graph.queued');
      case 'skipped':
        if (gate.reason === 'failed') {
          return t('repo.pipeline.graph.skipped_failed', { name: gate.on });
        }
        if (gate.reason === 'canceled') {
          return t('repo.pipeline.graph.skipped_canceled', { name: gate.on });
        }
        return t('repo.pipeline.graph.skipped_skipped', { name: gate.on });
    }
  }

  // stepGateText describes a step's gate; a step waiting for others reads "after <names>"
  function stepGateText(gate: Gate): string | undefined {
    if (gate.kind === 'waiting') {
      return t('repo.pipeline.graph.after', { names: names(gate.on, true) });
    }
    if (gate.kind === 'skipped') {
      return gateText(gate);
    }
    return undefined;
  }

  return { gateText, stepGateText };
}

const backgrounds = {
  red: 'bg-wp-error-100',
  green: 'bg-wp-state-ok-100',
  blue: 'bg-wp-state-info-100 animate-pulse',
  orange: 'bg-wp-state-warn-100',
  gray: 'bg-wp-state-neutral-100',
} as const;

// background colour of a status, for progress bars
export function statusBackground(state: PipelineStatus): string {
  if (state === 'pending' || state === 'blocked') {
    return 'bg-wp-state-neutral-100 opacity-30';
  }
  if (state === 'skipped') {
    return 'bg-wp-state-neutral-100 opacity-60';
  }
  return backgrounds[pipelineStatusColors[state]];
}

// border colour that makes failing and running cards stand out
export function statusBorder(state: PipelineStatus): string {
  const color = pipelineStatusColors[state];
  if (color === 'red') {
    return 'border-wp-error-100';
  }
  if (color === 'blue') {
    return 'border-wp-state-info-100';
  }
  return 'border-wp-background-400 dark:border-wp-background-100';
}
