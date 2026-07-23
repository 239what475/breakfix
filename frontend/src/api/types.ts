export interface Challenge {
  id: string;
  title: string;
  type: string;
  runtime: "container" | "vcluster";
  difficulty: "easy" | "medium" | "hard";
  tags: string[];
  description: string;
  solved: boolean;
  active: boolean;
}

export interface Checkpoint {
  id: string;
  title: string;
  description: string;
  hint?: string;
  depends_on?: string[];
}

export interface CheckpointResult {
  id: string;
  passed: boolean;
  summary: string;
  details?: string;
}

export interface ChallengeContent {
  id: string;
  title: string;
  problem: string;
  solution: string;
  hints: Record<string, string>;
  checkpoints: Checkpoint[];
}

export interface ChallengeDraft {
  title: string;
  difficulty: "easy" | "medium" | "hard";
  tags: string[];
  description: string;
  goal: string;
  symptoms: string;
  fault_mechanism: string;
  environment_shape: string;
  acceptance_criteria: string;
  difficulty_reason: string;
  notes?: string;
}

export interface GenerationJobResponse {
  job_id?: string;
  challenge_id?: string;
  status: string;
  message: string;
  started_at?: string;
  completed_at?: string;
}

export interface GenerateDraftResponse {
  status: string;
  verdict: string;
  reason: string;
  warnings?: string[];
  draft?: ChallengeDraft;
}
