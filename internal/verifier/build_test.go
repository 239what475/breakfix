package verifier

import "testing"

func TestBuildOutputInfrastructureClassification(t *testing.T) {
	for _, output := range []string{
		"failed to push image: connection refused",
		"failed to do request: i/o timeout",
		"registry response: 503 Service Unavailable",
	} {
		if !buildOutputIsInfrastructure(output) {
			t.Fatalf("expected infrastructure classification for %q", output)
		}
	}
	if buildOutputIsInfrastructure("failed to solve: Dockerfile parse error line 2") {
		t.Fatal("Dockerfile syntax error must be an artifact failure")
	}
}

func TestArtifactBuildError(t *testing.T) {
	err := &ArtifactBuildError{Err: errTestBuild}
	if !isArtifactBuildError(err) {
		t.Fatal("artifact build error was not recognized")
	}
}

var errTestBuild = testBuildError("build failed")

type testBuildError string

func (e testBuildError) Error() string { return string(e) }
