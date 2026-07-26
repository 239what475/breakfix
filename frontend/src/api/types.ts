import type { AuthoringSession as GeneratedAuthoringSession } from "./generated";

// JSON API models are generated from api/openapi.yaml. These aliases preserve
// concise feature-facing names without duplicating the server contract.
export type {
	AssistantConversation,
	AssistantEvidence,
	AssistantMessage,
	AssistantMessageRequest,
	AssistantTurn,
	AuthoringArtifact,
	AuthoringAsset,
	AuthoringChange,
	AuthoringCheckpoint,
	AuthoringFileDiff,
	AuthoringMessage,
	AuthoringMetadata,
	AuthoringPlan,
	AuthoringSession,
	AuthoringVerification,
	AuthoringVerificationIssue,
	AuthoringVerificationReport,
	ChallengeContent,
	CheckpointProgressSummary,
	CheckpointResult,
	MySpace,
	MySpaceActiveEnvironment,
	MySpaceAuthoring,
	MySpaceAuthoringDraft,
	MySpaceChallenge,
	MySpaceEnvironmentQuota,
	MySpaceLearningHistory,
	MySpaceLearningPage,
	MySpaceProfile,
	MySpacePublishedChallenge,
	MySpaceSummary,
	VerifiedChallenge,
	VerifiedCheckpoint,
} from "./generated";

export type {
	ChallengeCheckpoint as Checkpoint,
	ChallengeSummary as Challenge,
} from "./generated";

export type AuthoringState = GeneratedAuthoringSession["state"];

// Assistant streaming uses Server-Sent Events rather than an OpenAPI JSON
// response, so these client-side event envelopes remain local.
export interface AssistantStreamEvent {
	type: "ready" | "tool" | "delta" | "reset";
	turn_id?: string;
	content?: string;
	tool?: string;
	turn?: import("./generated").AssistantTurn;
}

export interface AssistantStreamComplete {
	turn_id: string;
	session_id: string;
	message: import("./generated").AssistantMessage;
}
