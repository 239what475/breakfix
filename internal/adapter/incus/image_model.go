package incus

type ImageFile struct {
	Path    string
	Content []byte
	Mode    int
}

type BuildNodeImageRequest struct {
	WorkflowID          string
	CandidateRevisionID string
	Attempt             int64
	Revision            string
	Files               []ImageFile
}

type BuildNodeImageResult struct {
	WorkflowID          string
	CandidateRevisionID string
	Attempt             int64
	InstanceName        string
	Alias               string
	Fingerprint         string
}

type PublishNodeImageRequest struct {
	CandidateRevisionID string
	Revision            string
	Build               BuildNodeImageResult
}

type PublishNodeImageResult struct {
	Alias       string
	Fingerprint string
}

type PublishChallengeNodeImageRequest struct {
	CandidateRevisionID string
	ChallengeID         string
	Staging             PublishNodeImageResult
	// ExpectedCurrentFingerprint permits an explicit image replacement.
	// Normal ChallengePublish leaves it empty and therefore never repoints an
	// existing formal alias.
	ExpectedCurrentFingerprint string
}
