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
	// BundlePath is the absolute runtime path where files are installed.
	BundlePath string
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
