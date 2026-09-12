import type { AuthoringSession as GeneratedAuthoringSession } from "./generated";

// JSON API models are generated from api/http/openapi.yaml. These aliases preserve
// concise feature-facing names without duplicating the server contract.
export type {
	AssistantConversation,
	AssistantEvidence,
	AssistantMessage,
	AssistantMessageRequest,
	AssistantTerminalContext,
	AuthoringAsset,
	AuthoringCandidate,
	AuthoringChange,
	AuthoringCheckpoint,
	AuthoringCheckpointResult,
	AuthoringExecutionResult,
	AuthoringFileDiff,
	AuthoringMessage,
	AuthoringMetadata,
	AuthoringPlan,
	AuthoringSession,
	AuthoringVerificationReport,
	GeneratorGeneration,
	GeneratorWorkflow,
	ChallengeContent,
	ChallengeRoadmap,
	ChallengeNode,
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
	RoadmapDomain,
	RoadmapEdge,
	RoadmapReference,
	RoadmapTag,
	RoadmapTopic,
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
	run_id: string;
	content?: string;
	tool?: string;
}

export interface AssistantStreamComplete {
	run_id: string;
	message: import("./generated").AssistantMessage;
}

export interface AuthoringStreamEvent {
	type: "ready" | "delta";
	run_id: string;
	content?: string;
}

export interface AuthoringStreamComplete {
	run_id: string;
	content: string;
}
