package runnable

import "testing"

func TestActionContextOnlyExposesPhaseSpecificInput(t *testing.T) {
	spec := validSpec()
	digest, err := spec.Digest()
	if err != nil {
		t.Fatal(err)
	}
	context := ActionContext{Credential: LeaseCredential{Identity: ActionIdentity{Content: spec.Identity, SpecDigest: digest, Phase: ActionMaterializeArtifact, StateVersion: 1}, LeaseOwner: "worker-01"}, Attempt: 1, Spec: &spec}
	if err := context.Validate(); err != nil {
		t.Fatalf("validate materialization context: %v", err)
	}
	context.Attempt = 0
	if err := context.Validate(); err == nil {
		t.Fatal("materialization context accepted a missing claim attempt")
	}
	context.Attempt = 1
	revision := validRevision(t, spec)
	context.RunnableRevision = &revision
	if err := context.Validate(); err == nil {
		t.Fatal("materialization context accepted a runnable revision")
	}
	context.RunnableRevision = nil
	context.Spec = nil
	context.Credential.Identity.Phase = ActionVerify
	context.RunnableRevision = &revision
	context.RunnableRevisionDigest, err = revision.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if err := context.Validate(); err == nil {
		t.Fatal("verification context accepted a missing revision reference")
	}
	context.RunnableRevisionRef = RevisionReference{ID: "revision-01", Digest: context.RunnableRevisionDigest}
	if err := context.Validate(); err != nil {
		t.Fatalf("verification context with revision reference: %v", err)
	}
}
