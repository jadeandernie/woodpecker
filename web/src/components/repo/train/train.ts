import type { StatusKind } from '~/components/repo/insights/stats';
import type { TrainEvent, TrainInvestigation, TrainPhase, TrainState } from '~/lib/api/types';

export function shortSha(sha: string | undefined): string {
  return (sha ?? '').slice(0, 8);
}

// the colour a phase gets, in the insights status palette; a bisect locks the
// branch too, but only a frozen train is red
export function phaseKind(phase: TrainPhase): StatusKind {
  switch (phase) {
    case 'frozen':
      return 'failure';
    case 'promoting':
      return 'running';
    case 'investigating':
      return 'pending';
    case 'held':
      return 'canceled';
    case 'idle':
      return 'success';
  }
}

export type CarMark = 'reverted' | 'culprit' | 'suspect';

// what the train knows about a car: reverted off the branch, found to break a
// workflow, or still among the candidates of an investigation
export function carMark(state: TrainState, sha: string): CarMark | undefined {
  if ((state.reverts ?? []).some((r) => r.car === sha)) {
    return 'reverted';
  }
  const investigations = state.investigations ?? [];
  if (investigations.some((inv) => inv.culprit === sha)) {
    return 'culprit';
  }
  if (investigations.some((inv) => inv.step !== 'done' && remaining(inv).includes(sha))) {
    return 'suspect';
  }
  return undefined;
}

// the candidates a bisect has not cleared yet: after the newest good one, up
// to and including the oldest bad one
export function remaining(inv: TrainInvestigation): string[] {
  if (inv.step === 'done') {
    return inv.culprit !== undefined && inv.culprit !== '' ? [inv.culprit] : [];
  }
  const bad = inv.bad >= 0 && inv.bad < inv.candidates.length ? inv.bad : inv.candidates.length - 1;
  return inv.candidates.slice(Math.max(inv.good + 1, 0), bad + 1);
}

export function newestFirst(events: TrainEvent[] | undefined): TrainEvent[] {
  return [...(events ?? [])].sort((a, b) => b.time - a.time);
}

// whether resume and retry make sense now
export function canResume(state: TrainState): boolean {
  return state.frozen || state.phase === 'frozen';
}

export function canRetry(state: TrainState): boolean {
  return !canResume(state) && (state.phase === 'idle' || state.phase === 'held');
}
