package runnable

import (
	"strings"
	"testing"
)

func TestActionIdentityIsStableAcrossLeaseTakeover(t *testing.T) {
	spec := validSpec()
	digest, err := spec.Digest()
	if err != nil {
		t.Fatal(err)
	}
	identity := ActionIdentity{Content: spec.Identity, SpecDigest: digest, Phase: ActionMaterializeArtifact, StateVersion: 3}
	first := LeaseCredential{Identity: identity, LeaseOwner: "worker-one"}
	second := LeaseCredential{Identity: identity, LeaseOwner: "worker-two"}
	if err := first.Validate(); err != nil {
		t.Fatal(err)
	}
	if first.Identity.Key() != second.Identity.Key() {
		t.Fatal("lease takeover changed the provider idempotency key")
	}
}

func TestMaterializeRequestBindsContentAndSpecDigest(t *testing.T) {
	spec := validSpec()
	digest, err := spec.Digest()
	if err != nil {
		t.Fatal(err)
	}
	request := MaterializeRequest{Credential: LeaseCredential{Identity: ActionIdentity{Content: spec.Identity, SpecDigest: digest, Phase: ActionMaterializeArtifact, StateVersion: 1}, LeaseOwner: "worker-01"}, Spec: spec}
	if err := request.Validate(); err != nil {
		t.Fatalf("validate request: %v", err)
	}
	request.Credential.Identity.SpecDigest = testDigest("f")
	if err := request.Validate(); err == nil || !strings.Contains(err.Error(), "does not match spec") {
		t.Fatalf("expected digest binding rejection, got %v", err)
	}
}

func TestVerifyRequestRequiresCompleteRevision(t *testing.T) {
	revision := validRevision(t, validSpec())
	specDigest, err := revision.Spec.Digest()
	if err != nil {
		t.Fatal(err)
	}
	revisionDigest, err := revision.Digest()
	if err != nil {
		t.Fatal(err)
	}
	request := VerifyRequest{
		Credential:       LeaseCredential{Identity: ActionIdentity{Content: revision.Spec.Identity, SpecDigest: specDigest, Phase: ActionVerify, StateVersion: 4}, LeaseOwner: "worker-01"},
		RunnableRevision: revision, RunnableRevisionDigest: revisionDigest, Attempt: 1,
	}
	if err := request.Validate(); err != nil {
		t.Fatalf("validate complete revision: %v", err)
	}
	request.RunnableRevisionDigest = testDigest("e")
	if err := request.Validate(); err == nil || !strings.Contains(err.Error(), "does not match revision") {
		t.Fatalf("expected revision binding rejection, got %v", err)
	}
}
