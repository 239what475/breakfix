package server

import (
	"strings"
	"testing"

	"github.com/breakfix/breakfix/internal/candidate"
	"github.com/breakfix/breakfix/internal/challenge"
	"github.com/breakfix/breakfix/internal/config"
	"github.com/breakfix/breakfix/internal/incusprovider"
)

func TestCandidateArtifactOwnershipMatchesClaimedResources(t *testing.T) {
	handler := NewHandler(nil, nil, config.Config{
		Registry: config.RegistryConfig{Address: "registry.breakfix.internal/breakfix"},
		Incus:    incusprovider.Config{NamePrefix: "bf"},
	})
	const fingerprint = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	t.Run("k8s staging and final artifacts", func(t *testing.T) {
		view := candidate.WorkerView{ID: "candidate-a", Snapshot: candidate.ExecutionSnapshot{Runtime: challenge.RuntimeK8s}}
		stagingRepository, err := candidate.CandidateOCIRepository(handler.registryAddr, view.ID)
		if err != nil {
			t.Fatal(err)
		}
		staging := candidate.ArtifactReference{Runtime: challenge.RuntimeK8s, OCIReference: stagingRepository + "@sha256:" + fingerprint}
		if err := handler.validateCandidateStagingArtifact(view, staging); err != nil {
			t.Fatalf("validate staging artifact: %v", err)
		}
		wrongRepository := candidate.ArtifactReference{Runtime: challenge.RuntimeK8s, OCIReference: "registry.breakfix.internal/breakfix/candidates/other@sha256:" + fingerprint}
		if err := handler.validateCandidateStagingArtifact(view, wrongRepository); err == nil {
			t.Fatal("accepted an OCI artifact from another candidate repository")
		}

		view.Artifact = &staging
		view.Publication = &candidate.Publication{ChallengeID: "challenge-a"}
		finalRepository, err := candidate.ChallengeOCIRepository(handler.registryAddr, view.Publication.ChallengeID)
		if err != nil {
			t.Fatal(err)
		}
		final := candidate.ArtifactReference{Runtime: challenge.RuntimeK8s, OCIReference: finalRepository + "@sha256:" + fingerprint}
		if err := handler.validateCandidateChallengeArtifact(view, final); err != nil {
			t.Fatalf("validate final artifact: %v", err)
		}
		wrongDigest := candidate.ArtifactReference{Runtime: challenge.RuntimeK8s, OCIReference: finalRepository + "@sha256:" + strings.Repeat("b", 64)}
		if err := handler.validateCandidateChallengeArtifact(view, wrongDigest); err == nil {
			t.Fatal("accepted a final OCI artifact with a different digest")
		}
	})

	t.Run("node staging and final artifacts", func(t *testing.T) {
		candidateAlias, err := incusprovider.AliasForCandidate("bf", "candidate-node")
		if err != nil {
			t.Fatal(err)
		}
		view := candidate.WorkerView{
			ID:       "candidate-node",
			Snapshot: candidate.ExecutionSnapshot{Runtime: challenge.RuntimeNode},
			Build:    &candidate.BuildOutput{Runtime: challenge.RuntimeNode, Incus: &candidate.IncusBuildReference{Fingerprint: fingerprint}},
		}
		staging := candidate.ArtifactReference{Runtime: challenge.RuntimeNode, IncusAlias: candidateAlias, IncusFingerprint: fingerprint}
		if err := handler.validateCandidateStagingArtifact(view, staging); err != nil {
			t.Fatalf("validate node staging artifact: %v", err)
		}
		view.Artifact = &staging
		view.Publication = &candidate.Publication{ChallengeID: "challenge-node"}
		challengeAlias, err := incusprovider.AliasForChallenge("bf", view.Publication.ChallengeID)
		if err != nil {
			t.Fatal(err)
		}
		final := candidate.ArtifactReference{Runtime: challenge.RuntimeNode, IncusAlias: challengeAlias, IncusFingerprint: fingerprint}
		if err := handler.validateCandidateChallengeArtifact(view, final); err != nil {
			t.Fatalf("validate node final artifact: %v", err)
		}
		wrongAlias := candidate.ArtifactReference{Runtime: challenge.RuntimeNode, IncusAlias: candidateAlias, IncusFingerprint: fingerprint}
		if err := handler.validateCandidateChallengeArtifact(view, wrongAlias); err == nil {
			t.Fatal("accepted a final Node artifact using the staging alias")
		}
	})
}
