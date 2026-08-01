package taxonomy

// ChallengeArtifact is the deterministic, read-only artifact document sent to
// taxonomy agents. It deliberately contains no host path or write capability.
type ChallengeArtifact struct {
	Files []ChallengeArtifactFile `json:"files"`
}

type ChallengeArtifactFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}
