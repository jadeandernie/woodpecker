// the merge train promotes a train branch (dev) to its target (main) once a promote pipeline is green
export interface TrainSettings {
  enabled: boolean;
  branch: string;
  target: string;
  // ignore workflows already red on the target's last promote or push pipeline
  ignore_base_failures: boolean;
  max_bisect_parallel: number;
  // lock the train branch through branch protection while the train is frozen or bisecting
  freeze_protection: boolean;
}

export type TrainPhase = 'idle' | 'promoting' | 'investigating' | 'held' | 'frozen';

export interface TrainCar {
  sha: string;
  parent?: string;
  title: string;
  pr?: number;
  files?: string[];
}

export interface TrainProbe {
  pipeline: number;
  sha: string;
  // -1 for a run at the train branch head
  index: number;
  result?: 'success' | 'failure' | 'inconclusive';
}

export interface TrainInvestigation {
  workflow: string;
  candidates: string[];
  step: 'recheck' | 'flake_check' | 'bisect' | 'done';
  good: number;
  bad: number;
  probes?: TrainProbe[];
  outcome?: 'culprit' | 'flake' | 'base_broken' | 'inconclusive';
  culprit?: string;
  detail?: string;
}

export interface TrainRevert {
  car: string;
  title: string;
  pr?: number;
  workflows: string[];
  promote: number;
  commit?: string;
  created: number;
}

export interface TrainEvent {
  time: number;
  kind: string;
  message: string;
  pipeline?: number;
  sha?: string;
}

export interface TrainState {
  phase: TrainPhase;
  frozen: boolean;
  frozen_reason: string;
  head: string;
  base: string;
  pipeline: number;
  cars?: TrainCar[];
  regressed?: string[];
  investigations?: TrainInvestigation[];
  held_head?: string;
  last_promoted?: string;
  last_promote_pipeline?: number;
  last_result?: string;
  reverts?: TrainRevert[];
  history?: TrainEvent[];
  updated: number;
}

export interface TrainStatus {
  settings: TrainSettings;
  has_bot_token: boolean;
  state: TrainState;
}
