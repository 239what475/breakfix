package httpapi

import (
	"testing"

	"github.com/breakfix/breakfix/internal/adapter/incus"
	"github.com/breakfix/breakfix/internal/bootstrap/config"
	"github.com/breakfix/breakfix/internal/content/candidate"
	"github.com/breakfix/breakfix/internal/content/challenge"
	"github.com/breakfix/breakfix/internal/domain/execution"
	runtime "github.com/breakfix/breakfix/internal/domain/runtime"
)

func TestRuntimeArtifactOwnershipAcceptsActionScopedReferences(t *testing.T) {
	handler := newHandlerForTest(t, nil, nil, config.Config{
		Registry: config.RegistryConfig{Repository: "registry.example.com/breakfix"},
		Incus:    incus.Config{NamePrefix: "bf"},
	})
	const fingerprint = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	stagingRepository, err := candidate.CandidateOCIRepository(handler.registryRepository, "candidate-k8s")
	if err != nil {
		t.Fatal(err)
	}
	staging := execution.ArtifactReference{Runtime: challenge.RuntimeK8s, OCIReference: stagingRepository + "@sha256:" + fingerprint}
	k8sAction := runtime.Context{Identity: runtime.Identity{CandidateID: "candidate-k8s"}, Snapshot: execution.Snapshot{Runtime: challenge.RuntimeK8s}}
	if err := handler.validateRuntimeStagingArtifact(k8sAction, staging); err != nil {
		t.Fatalf("validate K8s staging artifact: %v", err)
	}
	finalRepository, err := candidate.ChallengeOCIRepository(handler.registryRepository, "challenge-k8s", "chrev-aaaaaaaaaaaaaaaa")
	if err != nil {
		t.Fatal(err)
	}
	k8sAction.Artifact = &staging
	k8sAction.ChallengeID = "challenge-k8s"
	k8sAction.ChallengeRevisionID = "chrev-aaaaaaaaaaaaaaaa"
	if err := handler.validateRuntimeChallengeArtifact(k8sAction, execution.ArtifactReference{Runtime: challenge.RuntimeK8s, OCIReference: finalRepository + "@sha256:" + fingerprint}); err != nil {
		t.Fatalf("validate K8s final artifact: %v", err)
	}

	candidateAlias, err := incus.AliasForCandidate("bf", "candidate-node")
	if err != nil {
		t.Fatal(err)
	}
	nodeAction := runtime.Context{Identity: runtime.Identity{CandidateID: "candidate-node"}, Snapshot: execution.Snapshot{Runtime: challenge.RuntimeNode},
		Build: &execution.BuildOutput{Runtime: challenge.RuntimeNode, Incus: &execution.IncusBuildReference{Fingerprint: fingerprint}}}
	nodeStaging := execution.ArtifactReference{Runtime: challenge.RuntimeNode, IncusAlias: candidateAlias, IncusFingerprint: fingerprint}
	if err := handler.validateRuntimeStagingArtifact(nodeAction, nodeStaging); err != nil {
		t.Fatalf("validate Node staging artifact: %v", err)
	}
	challengeAlias, err := incus.AliasForChallenge("bf", "challenge-node", "chrev-bbbbbbbbbbbbbbbb")
	if err != nil {
		t.Fatal(err)
	}
	nodeAction.Artifact = &nodeStaging
	nodeAction.ChallengeID = "challenge-node"
	nodeAction.ChallengeRevisionID = "chrev-bbbbbbbbbbbbbbbb"
	if err := handler.validateRuntimeChallengeArtifact(nodeAction, execution.ArtifactReference{Runtime: challenge.RuntimeNode, IncusAlias: challengeAlias, IncusFingerprint: fingerprint}); err != nil {
		t.Fatalf("validate Node final artifact: %v", err)
	}
}
