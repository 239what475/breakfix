package fakecontent

import (
	"context"
	"testing"

	"github.com/breakfix/breakfix/internal/domain/runnable"
	runnableworker "github.com/breakfix/breakfix/internal/worker/runnable"
)

func TestFakeContentRunsThroughPublicWorkerWithoutProductBranch(t *testing.T) {
	spec := Spec()
	if err := spec.Validate(); err != nil {
		t.Fatalf("fake spec: %v", err)
	}
	specDigest, err := spec.Digest()
	if err != nil {
		t.Fatal(err)
	}
	worker, err := runnableworker.New(Materializer{}, Verifier{})
	if err != nil {
		t.Fatal(err)
	}
	credential := runnable.LeaseCredential{Identity: runnable.ActionIdentity{
		Content: spec.Identity, SpecDigest: specDigest, Phase: runnable.ActionMaterializeArtifact, StateVersion: 1,
	}, LeaseOwner: "fake-worker"}
	revision, err := worker.Materialize(context.Background(), runnable.MaterializeRequest{Credential: credential, Spec: spec})
	if err != nil {
		t.Fatalf("materialize fake content: %v", err)
	}
	if revision.Spec.Identity.Kind != contentKind || revision.Artifact.BuiltFromSpecDigest != specDigest {
		t.Fatalf("materialized fake revision = %#v", revision)
	}
	revisionDigest, err := revision.Digest()
	if err != nil {
		t.Fatal(err)
	}
	report, err := worker.Verify(context.Background(), runnable.VerifyRequest{
		Credential: runnable.LeaseCredential{Identity: runnable.ActionIdentity{
			Content: spec.Identity, SpecDigest: specDigest, Phase: runnable.ActionVerify, StateVersion: 2,
		}, LeaseOwner: "fake-worker"},
		RunnableRevision: revision, RunnableRevisionRef: runnable.RevisionReference{ID: "fake-revision", Digest: revisionDigest},
		RunnableRevisionDigest: revisionDigest, Attempt: 1,
	})
	if err != nil {
		t.Fatalf("verify fake content: %v", err)
	}
	if !report.Passed || report.Environment.Provider != "fake" {
		t.Fatalf("fake verification report = %#v", report)
	}
}
