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

export type AuthoringState =
  | "DraftConversation"
  | "IntentReview"
  | "GeneratingAndVerifying"
  | "AwaitingVerifiedReview"
  | "RevisingAndVerifying"
  | "Publishing"
  | "Published";

export interface AuthoringMetadata {
  title: string;
  difficulty: "easy" | "medium" | "hard";
  tags: string[];
  description: string;
  runtime: "container" | "vcluster";
}

export interface AuthoringCheckpoint {
  id: string;
  title: string;
  markdown: string;
  position: number;
}

export interface AuthoringPlan {
  metadata: AuthoringMetadata;
  overview: string;
  checkpoints: AuthoringCheckpoint[];
}

export interface VerifiedCheckpoint {
  id: string;
  title: string;
  description: string;
  hint?: string;
  depends_on?: string[];
}

export interface VerifiedChallenge {
  metadata: AuthoringMetadata;
  checkpoints: VerifiedCheckpoint[];
}

export interface AuthoringArtifact {
  submission_id: string;
  directory: string;
  generation_id: string;
}

export interface AuthoringVerification {
  task_id: string;
  phase: string;
  message: string;
  report?: AuthoringVerificationReport;
  challenge_id?: string;
}

export interface AuthoringVerificationReport {
  build_passed: boolean;
  answer_passed: boolean;
  checkpoints_passed: boolean;
  summary?: string;
  issues?: AuthoringVerificationIssue[];
}

export interface AuthoringVerificationIssue {
  code: string;
  message: string;
}

export interface AuthoringChange {
  kind: string;
  summary: string;
  difficulty_impact: string;
  revision: number;
}

export interface AuthoringMessage {
  id: string;
  role: "user" | "agent" | "system" | "event";
  content: string;
  changes?: AuthoringChange[];
  created_at: string;
}

export interface AuthoringAsset {
  path: string;
  content: string;
}

export interface AuthoringFileDiff {
  path: string;
  diff: string;
}

export interface AuthoringSession {
  id: string;
  state: AuthoringState;
  intent_revision: number;
  visible_revision: number;
  generation_id?: string;
  verify_task_id?: string;
  updated_at?: string;
  intent: AuthoringPlan;
  artifact?: AuthoringArtifact;
  verified?: VerifiedChallenge;
  verification?: AuthoringVerification;
  messages: AuthoringMessage[];
  assets: AuthoringAsset[];
  diff: AuthoringFileDiff[];
}
