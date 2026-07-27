package verifier

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/breakfix/breakfix/internal/registry"
)

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

func TestRegistryDockerConfigUsesExactRegistryHost(t *testing.T) {
	config, err := registryDockerConfig("registry.breakfix.internal", registry.Credentials{Username: "verifier", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Auths map[string]struct {
			Auth string `json:"auth"`
		} `json:"auths"`
	}
	if err := json.Unmarshal(config, &decoded); err != nil {
		t.Fatal(err)
	}
	if got := decoded.Auths["registry.breakfix.internal"].Auth; got != base64.StdEncoding.EncodeToString([]byte("verifier:secret")) {
		t.Fatalf("registry auth = %q", got)
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
