package candidate

import "testing"

func TestCandidateAndScenarioOCIReferencesUseDistinctOpaqueScopes(t *testing.T) {
	candidateID := "candidate-3efac8f8"
	scenarioID := "scenario-b8caf76c"
	scenarioRevisionID := "chrev-0123456789abcdef"

	candidateRef, err := CandidateOCIImageReference("registry.example.com/breakfix", candidateID)
	if err != nil {
		t.Fatal(err)
	}
	scenarioRef, err := ScenarioOCIImageReference("registry.example.com/breakfix", scenarioID, scenarioRevisionID)
	if err != nil {
		t.Fatal(err)
	}
	if candidateRef != "registry.example.com/breakfix/candidates/"+OpaqueName(candidateID)+":artifact" {
		t.Fatalf("candidate image reference = %q", candidateRef)
	}
	if scenarioRef != "registry.example.com/breakfix/scenarios/"+OpaqueName(scenarioID+"\x00"+scenarioRevisionID)+":published" {
		t.Fatalf("scenario image reference = %q", scenarioRef)
	}
	if candidateRef == scenarioRef {
		t.Fatalf("candidate and scenario references collide: %q", candidateRef)
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
