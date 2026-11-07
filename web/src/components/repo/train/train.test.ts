import { describe, expect, it } from 'vitest';

import type { TrainInvestigation, TrainState } from '~/lib/api/types';

import { canResume, canRetry, carMark, newestFirst, phaseKind, remaining, shortSha } from './train';

function state(patch: Partial<TrainState> = {}): TrainState {
  return { phase: 'idle', frozen: false, frozen_reason: '', head: '', base: '', pipeline: 0, updated: 0, ...patch };
}

function bisect(patch: Partial<TrainInvestigation> = {}): TrainInvestigation {
  return { workflow: 'test', candidates: ['a', 'b', 'c', 'd', 'e'], step: 'bisect', good: -1, bad: 4, ...patch };
}

describe('train', () => {
  it('shortens shas', () => {
    expect(shortSha('0123456789abcdef')).toBe('01234567');
    expect(shortSha(undefined)).toBe('');
  });

  it('colours phases', () => {
    expect(phaseKind('idle')).toBe('success');
    expect(phaseKind('promoting')).toBe('running');
    expect(phaseKind('investigating')).toBe('pending');
    expect(phaseKind('held')).toBe('canceled');
    expect(phaseKind('frozen')).toBe('failure');
  });

  it('narrows the remaining candidates', () => {
    expect(remaining(bisect())).toEqual(['a', 'b', 'c', 'd', 'e']);
    expect(remaining(bisect({ good: 1, bad: 3 }))).toEqual(['c', 'd']);
    expect(remaining(bisect({ step: 'done', culprit: 'c' }))).toEqual(['c']);
    expect(remaining(bisect({ step: 'done', outcome: 'flake' }))).toEqual([]);
  });

  it('marks cars', () => {
    const st = state({
      reverts: [{ car: 'a', title: 'A', workflows: ['test'], promote: 3, created: 0 }],
      investigations: [bisect({ good: 1, bad: 3 }), bisect({ workflow: 'lint', step: 'done', culprit: 'e' })],
    });
    expect(carMark(st, 'a')).toBe('reverted');
    expect(carMark(st, 'e')).toBe('culprit');
    expect(carMark(st, 'c')).toBe('suspect');
    expect(carMark(st, 'b')).toBeUndefined();
  });

  it('orders history newest first', () => {
    const events = [
      { time: 1, kind: 'promote', message: 'one' },
      { time: 3, kind: 'revert', message: 'three' },
      { time: 2, kind: 'sync', message: 'two' },
    ];
    expect(newestFirst(events).map((e) => e.time)).toEqual([3, 2, 1]);
    expect(events[0].time).toBe(1);
  });

  it('offers resume when frozen and retry when idle or held', () => {
    expect(canResume(state({ phase: 'frozen', frozen: true }))).toBe(true);
    expect(canRetry(state({ phase: 'frozen', frozen: true }))).toBe(false);
    expect(canRetry(state({ phase: 'held' }))).toBe(true);
    expect(canRetry(state({ phase: 'promoting' }))).toBe(false);
    expect(canResume(state({ phase: 'investigating', frozen: true }))).toBe(true);
  });
});
