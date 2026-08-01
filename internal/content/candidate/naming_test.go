package candidate

import "testing"

func TestCandidateAndChallengeOCIReferencesUseDistinctOpaqueScopes(t *testing.T) {
	candidateID := "candidate-3efac8f8"
	challengeID := "challenge-b8caf76c"

	candidateRef, err := CandidateOCIImageReference("registry.example.com/breakfix", candidateID)
	if err != nil {
		t.Fatal(err)
	}
	challengeRef, err := ChallengeOCIImageReference("registry.example.com/breakfix", challengeID)
	if err != nil {
		t.Fatal(err)
	}
	if candidateRef != "registry.example.com/breakfix/candidates/"+OpaqueName(candidateID)+":artifact" {
		t.Fatalf("candidate image reference = %q", candidateRef)
	}
	if challengeRef != "registry.example.com/breakfix/challenges/"+OpaqueName(challengeID)+":published" {
		t.Fatalf("challenge image reference = %q", challengeRef)
	}
	if candidateRef == challengeRef {
		t.Fatalf("candidate and challenge references collide: %q", candidateRef)
	}
}

func TestOCIReferencePartsPreserveImmutableIdentity(t *testing.T) {
	const reference = "registry.example.com/breakfix/candidates/abc@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	repository, err := OCIRepository(reference)
	if err != nil {
		t.Fatal(err)
	}
	if repository != "registry.example.com/breakfix/candidates/abc" {
		t.Fatalf("OCI repository = %q", repository)
	}
	digest, err := OCIDigest(reference)
	if err != nil {
		t.Fatal(err)
	}
	if digest != "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("OCI digest = %q", digest)
	}
}
