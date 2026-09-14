package runnable

import "testing"

func TestActionContextOnlyExposesPhaseSpecificInput(t *testing.T) {
	spec := validSpec()
	digest, err := spec.Digest()
	if err != nil {
		t.Fatal(err)
	}
	context := ActionContext{Credential: LeaseCredential{Identity: ActionIdentity{Content: spec.Identity, SpecDigest: digest, Phase: ActionMaterializeArtifact, StateVersion: 1}, LeaseOwner: "worker-01"}, Spec: &spec}
	if err := context.Validate(); err != nil {
		t.Fatalf("validate materialization context: %v", err)
	}
	revision := validRevision(t, spec)
	context.RunnableRevision = &revision
	if err := context.Validate(); err == nil {
		t.Fatal("materialization context accepted a runnable revision")
	}
}
