package generation

import "testing"

func TestResourceReapKindsHaveOneOwner(t *testing.T) {
	for _, kind := range []ResourceReapKind{
		ResourceReapVerificationEnvironment,
		ResourceReapNodeBuildImage,
		ResourceReapCandidateArtifact,
	} {
		if !kind.Valid() || kind.Owner() == "" {
			t.Fatalf("resource reap kind is not owned: %q", kind)
		}
	}
	if ResourceReapKind("unknown").Valid() || ResourceReapKind("unknown").Owner() != "" {
		t.Fatal("unknown resource reap kind was accepted")
	}
}
