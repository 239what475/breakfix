package generation

// GeneratedCandidate is the immutable archive emitted by a generator run.
type GeneratedCandidate struct {
	RunID         string `json:"run_id"`
	Archive       []byte `json:"archive"`
	ArchiveSHA256 string `json:"archive_sha256,omitempty"`
}

// Judgement is the Judge's decision over one generated candidate.
type Judgement struct {
	RunID    string `json:"run_id"`
	Approved bool   `json:"approved"`
	Feedback string `json:"feedback,omitempty"`
}

type Classification struct {
	RunID  string               `json:"run_id"`
	Output ClassificationOutput `json:"output"`
}

type BuildResult struct {
	Output  BuildOutput `json:"output"`
	Archive []byte      `json:"archive,omitempty"`
}

type ArtifactPublishResult struct {
	Artifact ArtifactReference `json:"artifact"`
}

type VerificationEnvironmentResult struct {
	Environment VerificationEnvironment `json:"environment"`
}

type VerificationResult struct {
	Report VerificationReport `json:"report"`
}

type ChallengePublishResult struct {
	Artifact ArtifactReference `json:"artifact"`
}

type InfrastructureFailureResult struct {
	Failure Failure `json:"failure"`
}

type ArtifactFailureResult struct {
	Failure Failure             `json:"failure"`
	Report  *VerificationReport `json:"report,omitempty"`
}
