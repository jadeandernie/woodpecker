// Derivations for the repo insights tab: group roll-ups, failing streaks,
// pipeline timings and summary statistics. Plain functions over the
// /api/repos/{id}/stats response so they can be unit tested without a server.
import type { PipelineStatus, StatsPipeline, StatsWorkflow } from '~/lib/api/types';

// The colour family a status is drawn in on the insights tab.
export type StatusKind = 'failure' | 'running' | 'pending' | 'success' | 'canceled' | 'skipped';

const statusKinds: Record<PipelineStatus, StatusKind> = {
  failure: 'failure',
  error: 'failure',
  killed: 'failure',
  declined: 'failure',
  running: 'running',
  started: 'running',
  pending: 'pending',
  blocked: 'pending',
  success: 'success',
  canceled: 'canceled',
  skipped: 'skipped',
};

// worst wins when rolling workflows up into a group cell
const kindRank: Record<StatusKind, number> = {
  failure: 5,
  running: 4,
  pending: 3,
  success: 2,
  canceled: 1,
  skipped: 0,
};

export function statusKind(status: PipelineStatus | string | undefined): StatusKind {
  return statusKinds[status as PipelineStatus] ?? 'skipped';
}

// The roll-up of several workflow states: failure/error/killed > running > pending/blocked > success > canceled > skipped.
export function rollUpStatus(states: PipelineStatus[]): PipelineStatus | undefined {
  let worst: PipelineStatus | undefined;
  for (const state of states) {
    if (worst === undefined || kindRank[statusKind(state)] > kindRank[statusKind(worst)]) {
      worst = state;
    }
  }
  return worst;
}

export const knownGroups = ['build', 'test', 'deploy'] as const;
export const otherGroup = 'other';

export function normalizeGroup(group: string | undefined): string {
  const g = (group ?? '').trim().toLowerCase();
  return g === '' ? otherGroup : g;
}

export function normalizeSubgroup(subgroup: string | undefined): string {
  return (subgroup ?? '').trim();
}

// build, test, deploy first, then any custom groups by name, other last
export function compareGroups(a: string, b: string): number {
  const rank = (g: string) => {
    const i = (knownGroups as readonly string[]).indexOf(g);
    if (i >= 0) {
      return i;
    }
    return g === otherGroup ? 1000 : 100;
  };
  return rank(a) - rank(b) || a.localeCompare(b);
}

export interface HistoryCell {
  pipeline: StatsPipeline;
  status: PipelineStatus;
  kind: StatusKind;
  workflows: StatsWorkflow[];
}

export interface HistoryRow {
  key: string;
  group: string;
  subgroup: string; // '' for a group row and for workflows without a subject
  // one entry per column (oldest first), undefined when the pipeline did not run this group
  cells: (HistoryCell | undefined)[];
  current?: HistoryCell; // the newest non-empty cell
  runs: number; // non-empty cells
  passed: number;
  failed: number;
  passRate?: number; // passed / (passed + failed), undefined without finished runs
  failingStreak: number; // consecutive failed runs, newest first, ignoring runs in progress and canceled/skipped ones
  children: HistoryRow[];
}

// Pipelines oldest first, the column order of the history views (newest right).
export function chronological(pipelines: StatsPipeline[]): StatsPipeline[] {
  return pipelines.toSorted((a, b) => a.number - b.number || a.created - b.created);
}

function makeRow(
  key: string,
  group: string,
  subgroup: string,
  columns: StatsPipeline[],
  pick: (w: StatsWorkflow) => boolean,
): HistoryRow {
  const cells = columns.map((pipeline): HistoryCell | undefined => {
    const workflows = (pipeline.workflows ?? []).filter(pick);
    const status = rollUpStatus(workflows.map((w) => w.state));
    if (status === undefined) {
      return undefined;
    }
    return { pipeline, status, kind: statusKind(status), workflows };
  });

  const filled = cells.filter((c): c is HistoryCell => c !== undefined);
  const passed = filled.filter((c) => c.kind === 'success').length;
  const failed = filled.filter((c) => c.kind === 'failure').length;

  return {
    key,
    group,
    subgroup,
    cells,
    current: filled.at(-1),
    runs: filled.length,
    passed,
    failed,
    passRate: passed + failed > 0 ? passed / (passed + failed) : undefined,
    failingStreak: failingStreak(filled.map((c) => c.kind)),
    children: [],
  };
}

// kinds oldest first; counts the failures at the newest end, stepping over runs
// that are still in progress or were canceled/skipped, stopping at a success
export function failingStreak(kinds: StatusKind[]): number {
  let streak = 0;
  for (let i = kinds.length - 1; i >= 0; i--) {
    const kind = kinds[i];
    if (kind === 'failure') {
      streak++;
    } else if (kind === 'success') {
      break;
    }
  }
  return streak;
}

// failing streaks first (longest first), then currently failing, then by name
export function compareRows(a: HistoryRow, b: HistoryRow): number {
  if (a.failingStreak !== b.failingStreak) {
    return b.failingStreak - a.failingStreak;
  }
  const af = a.current?.kind === 'failure' ? 1 : 0;
  const bf = b.current?.kind === 'failure' ? 1 : 0;
  if (af !== bf) {
    return bf - af;
  }
  if (a.subgroup === '' || b.subgroup === '') {
    // the "no subgroup" row goes last
    return (a.subgroup === '' ? 1 : 0) - (b.subgroup === '' ? 1 : 0);
  }
  return a.subgroup.localeCompare(b.subgroup);
}

// Group rows (build, test, deploy, ..., other) with their subgroup rows as
// children, over the given columns (oldest first).
export function buildHistoryRows(columns: StatsPipeline[]): HistoryRow[] {
  const groups = new Map<string, Set<string>>();
  for (const pipeline of columns) {
    for (const workflow of pipeline.workflows ?? []) {
      const group = normalizeGroup(workflow.group);
      const subgroups = groups.get(group) ?? new Set<string>();
      subgroups.add(normalizeSubgroup(workflow.subgroup));
      groups.set(group, subgroups);
    }
  }

  return [...groups.keys()].sort(compareGroups).map((group) => {
    const row = makeRow(group, group, '', columns, (w) => normalizeGroup(w.group) === group);
    row.children = [...(groups.get(group) ?? [])]
      .map((subgroup) =>
        makeRow(
          `${group}/${subgroup}`,
          group,
          subgroup,
          columns,
          (w) => normalizeGroup(w.group) === group && normalizeSubgroup(w.subgroup) === subgroup,
        ),
      )
      .sort(compareRows);
    return row;
  });
}

// --- timings ---------------------------------------------------------------

const isSet = (t: number | undefined): t is number => t !== undefined && t > 0;

// finished - started, in seconds; undefined while running or when it never ran
export function pipelineRuntime(p: StatsPipeline): number | undefined {
  if (!isSet(p.started) || !isSet(p.finished)) {
    return undefined;
  }
  return Math.max(0, p.finished - p.started);
}

// How long the pipeline waited for an agent: first workflow start - created.
// A pipeline still pending has waited now - created so far. undefined when it
// never started (skipped, declined, blocked, error before start).
export function pipelineQueued(p: StatsPipeline, now: number): number | undefined {
  const starts = (p.workflows ?? []).map((w) => w.started).filter(isSet);
  let firstStart = starts.length > 0 ? Math.min(...starts) : undefined;
  if (firstStart === undefined && isSet(p.started)) {
    // workflows without timestamps: fall back to the pipeline start
    firstStart = p.started;
  }
  if (firstStart !== undefined) {
    return Math.max(0, firstStart - p.created);
  }
  if (p.status === 'pending' && isSet(p.created)) {
    return Math.max(0, now - p.created);
  }
  return undefined;
}

// peak memory in bytes; undefined when not measured (agents older than this release report 0)
export function pipelineMemory(p: StatsPipeline): number | undefined {
  return isSet(p.memory_peak) ? p.memory_peak : undefined;
}

// Wall time of a group within a pipeline: from its first workflow start to its
// last workflow finish. undefined while any of them is still running.
export function groupRuntime(workflows: StatsWorkflow[]): number | undefined {
  if (workflows.length === 0) {
    return undefined;
  }
  const started = workflows.map((w) => w.started).filter(isSet);
  if (started.length === 0) {
    return undefined;
  }
  let last = 0;
  for (const w of workflows) {
    if (isSet(w.started) && !isSet(w.finished)) {
      return undefined; // still running
    }
    if (isSet(w.finished)) {
      last = Math.max(last, w.finished);
    }
  }
  return Math.max(0, last - Math.min(...started));
}

// --- statistics ------------------------------------------------------------

// linear interpolation between closest ranks (the same as numpy's default)
export function percentile(values: number[], p: number): number | undefined {
  if (values.length === 0) {
    return undefined;
  }
  const sorted = values.toSorted((a, b) => a - b);
  const pos = (sorted.length - 1) * Math.min(1, Math.max(0, p));
  const lo = Math.floor(pos);
  const hi = Math.ceil(pos);
  return sorted[lo] + (sorted[hi] - sorted[lo]) * (pos - lo);
}

export interface Summary {
  latest?: number;
  median?: number;
  p90?: number;
  count: number;
}

// values oldest first, undefined = not measured
export function summarize(values: (number | undefined)[]): Summary {
  const measured = values.filter((v): v is number => v !== undefined);
  return {
    latest: measured.at(-1),
    median: percentile(measured, 0.5),
    p90: percentile(measured, 0.9),
    count: measured.length,
  };
}

export interface GroupTiming {
  group: string;
  summary: Summary;
  values: (number | undefined)[];
}

// runtime per group over the columns (oldest first)
export function groupTimings(rows: HistoryRow[]): GroupTiming[] {
  return rows.map((row) => {
    const values = row.cells.map((c) => (c ? groupRuntime(c.workflows) : undefined));
    return { group: row.group, values, summary: summarize(values) };
  });
}

// --- formatting & axes -------------------------------------------------------

export function formatDuration(seconds: number | undefined): string {
  if (seconds === undefined || !Number.isFinite(seconds)) {
    return '–';
  }
  const s = Math.round(seconds);
  if (s < 60) {
    return `${s}s`;
  }
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const sec = s % 60;
  if (h > 0) {
    return m > 0 ? `${h}h ${m}m` : `${h}h`;
  }
  return sec > 0 ? `${m}m ${sec.toString().padStart(2, '0')}s` : `${m}m`;
}

const MiB = 1024 * 1024;
const GiB = 1024 * MiB;

export function formatBytes(bytes: number | undefined): string {
  if (bytes === undefined || !Number.isFinite(bytes)) {
    return '–';
  }
  if (bytes <= 0) {
    return '0';
  }
  if (bytes >= GiB) {
    const v = bytes / GiB;
    return `${v >= 10 ? v.toFixed(0) : v.toFixed(1)} GiB`;
  }
  if (bytes >= MiB) {
    const v = bytes / MiB;
    return `${v >= 10 ? v.toFixed(0) : v.toFixed(1)} MiB`;
  }
  return `${Math.max(0, Math.round(bytes / 1024))} KiB`;
}

export function formatPercent(ratio: number | undefined): string {
  return ratio === undefined ? '–' : `${Math.round(ratio * 100)}%`;
}

export type AxisUnit = 'duration' | 'bytes';

const durationSteps = [1, 2, 5, 10, 15, 30, 60, 120, 300, 600, 900, 1800, 3600, 7200, 10800, 21600, 43200, 86400];
const byteSteps = [
  ...[1, 2, 4, 8, 16, 32, 64, 128, 256, 512].map((n) => n * MiB),
  ...[1, 2, 4, 8, 16, 32, 64, 128, 256].map((n) => n * GiB),
];

// Evenly spaced round ticks from 0 that cover max with at most about `count` steps.
export function niceTicks(max: number, unit: AxisUnit, count = 4): number[] {
  if (!(max > 0)) {
    return [0];
  }
  const steps = unit === 'duration' ? durationSteps : byteSteps;
  const step = steps.find((s) => max / s <= count) ?? steps.at(-1)! * Math.ceil(max / count / steps.at(-1)!);
  const ticks: number[] = [];
  for (let v = 0; v < max + step; v += step) {
    ticks.push(v);
    if (v >= max) {
      break;
    }
  }
  return ticks;
}

// Tailwind classes per status family, the same colours as PipelineStatusIcon
export const kindBgClass: Record<StatusKind, string> = {
  failure: 'bg-wp-error-100',
  running: 'bg-wp-state-info-100',
  pending: 'bg-wp-state-warn-100',
  success: 'bg-wp-state-ok-100',
  canceled: 'bg-wp-state-neutral-100',
  skipped: 'bg-wp-state-neutral-100 opacity-40',
};

export const kindTextClass: Record<StatusKind, string> = {
  failure: 'text-wp-error-100',
  running: 'text-wp-state-info-100',
  pending: 'text-wp-state-warn-100',
  success: 'text-wp-state-ok-100',
  canceled: 'text-wp-state-neutral-100',
  skipped: 'text-wp-state-neutral-100',
};
