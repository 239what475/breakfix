export interface Challenge {
  id: string;
  title: string;
  type: string;
  runtime: "container" | "vcluster";
  difficulty: "easy" | "medium" | "hard";
  tags: string[];
  description: string;
  published_at: string;
  solved?: boolean;
  active?: boolean;
  progress?: CheckpointProgressSummary;
}

export interface CheckpointProgressSummary {
  passed: number;
  total: number;
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

export interface AssistantEvidence {
  kind: string;
  label: string;
}

export interface AssistantMessage {
  id: string;
  role: "user" | "assistant";
  content: string;
  evidence?: AssistantEvidence[];
  created_at: string;
}

export interface AssistantTurn {
  id: string;
  session_id: string;
  status: "running" | "completed" | "failed";
  content: string;
  evidence?: AssistantEvidence[];
  error?: string;
  message?: AssistantMessage;
  created_at: string;
  updated_at: string;
}

export interface AssistantConversation {
  id: string;
  challenge_id: string;
  messages: AssistantMessage[];
  active_turn?: AssistantTurn;
}

export interface AssistantMessageRequest {
  content: string;
  current_window: string;
  open_windows: string[];
}

export interface AssistantStreamEvent {
  type: "ready" | "tool" | "delta";
  turn_id?: string;
  content?: string;
  tool?: string;
  turn?: AssistantTurn;
}

export interface AssistantStreamComplete {
  turn_id: string;
  session_id: string;
  message: AssistantMessage;
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

export interface MySpaceChallenge {
  id: string;
  title: string;
  runtime: "container" | "vcluster";
  difficulty: string;
}

export interface MySpaceProfile {
  id: string;
  name: string;
  created_at: string;
}

export interface MySpaceEnvironmentQuota {
  occupied: number;
  maximum: number | null;
}

export interface MySpaceSummary {
  completed_count: number;
  attempted_count: number;
  in_progress_environment_count: number;
  terminal_learning_seconds: number;
  authoring_count: number;
  published_count: number;
  environment_quota: MySpaceEnvironmentQuota;
}

export interface MySpaceActiveEnvironment {
  environment_id: string;
  challenge: MySpaceChallenge;
  runtime: "container" | "vcluster";
  phase: string;
  checkpoint_progress: CheckpointProgressSummary;
  expires_at?: string;
}

export interface MySpaceLearningHistory {
  challenge: MySpaceChallenge;
  ready_at: string;
  completed_at?: string;
  learning_seconds: number;
  state: "active" | "completed" | "stopped" | "reset" | "expired";
}

export interface MySpaceAuthoringDraft {
  session_id: string;
  title: string;
  state: AuthoringState;
  updated_at: string;
}

export interface MySpacePublishedChallenge {
  challenge: MySpaceChallenge;
  published_at: string;
  attempted_users: number;
  completed_users: number;
  pass_rate?: number;
}

export interface MySpaceAuthoring {
  drafts: MySpaceAuthoringDraft[];
  published: MySpacePublishedChallenge[];
}

export interface MySpace {
  profile: MySpaceProfile;
  summary: MySpaceSummary;
  active_environments: MySpaceActiveEnvironment[];
  recent_learning: MySpaceLearningHistory[];
  recent_learning_next_cursor: string | null;
  authoring: MySpaceAuthoring;
}

export interface MySpaceLearningPage {
  items: MySpaceLearningHistory[];
  next_cursor: string | null;
}
