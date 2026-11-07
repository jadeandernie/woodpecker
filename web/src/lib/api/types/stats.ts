import type { PipelineStatus } from './pipeline';

// One workflow of a pipeline in the repo stats history.
export interface StatsWorkflow {
  id: number;
  name: string;
  group: string; // build, test, deploy, other
  subgroup: string; // knowledge, tensor, ... or '' when the name has no subject
  state: PipelineStatus;
  started?: number; // unix seconds, 0 when it never started
  finished?: number;
  depends_on?: string[];
  memory_peak?: number; // bytes, the highest of its steps; 0 when not measured
}

// One pipeline in the repo stats history, newest first.
export interface StatsPipeline {
  id: number;
  number: number;
  status: PipelineStatus;
  event: string;
  branch: string;
  commit: string;
  message: string;
  created: number; // unix seconds
  started?: number;
  finished?: number;
  memory_peak?: number; // bytes, the highest of any step in the pipeline; 0 when not measured
  workflows: StatsWorkflow[];
}

export interface RepoStats {
  branch: string; // the branch the history is for ('' for all branches)
  pipelines: StatsPipeline[];
}

export interface WorkflowGroupRule {
  pattern: string; // a Go regular expression matched against the workflow name
  group: string; // template, may use $1 / ${name} from the pattern
  subgroup?: string; // template, may use $1 / ${name} from the pattern
}
