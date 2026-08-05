package publish

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/adapter/oci"
	"github.com/breakfix/breakfix/internal/content/challenge"
	domainexecution "github.com/breakfix/breakfix/internal/domain/execution"
)

const publishTestDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestPublishArtifactReusesAnExistingImmutableTarget(t *testing.T) {
	registry := &fakeRegistry{resolved: map[string]string{
		"registry.example/candidates/placeholder:artifact": "registry.example/candidates/placeholder@" + publishTestDigest,
	}}
	work := publishTestWork()
	target, err := (&Executor{registryRepository: "registry.example"}).candidateImage(work.CandidateID)
	if err != nil {
		t.Fatal(err)
	}
	registry.resolved[target] = target[:strings.IndexByte(target, ':')] + "@" + publishTestDigest
	executor, err := NewExecutor(registry, nil, "registry.example")
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := executor.PublishArtifactWork(context.Background(), work)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.OCIReference != registry.resolved[target] || registry.copyCalls != 0 {
		t.Fatalf("reused artifact = %#v, copy calls = %d", artifact, registry.copyCalls)
	}
}

func TestPublishArtifactCreatesAndResolvesFreshTarget(t *testing.T) {
	registry := &fakeRegistry{resolved: map[string]string{}}
	work := publishTestWork()
	executor, err := NewExecutor(registry, nil, "registry.example")
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := executor.PublishArtifactWork(context.Background(), work)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.OCIReference == "" || registry.copyCalls != 1 {
		t.Fatalf("fresh artifact = %#v, copy calls = %d", artifact, registry.copyCalls)
	}
}

func TestPublishArtifactRejectsAConflictingExistingTarget(t *testing.T) {
	registry := &fakeRegistry{}
	work := publishTestWork()
	executor, err := NewExecutor(registry, nil, "registry.example")
	if err != nil {
		t.Fatal(err)
	}
	target, err := executor.candidateImage(work.CandidateID)
	if err != nil {
		t.Fatal(err)
	}
	registry.resolved = map[string]string{target: "registry.example/candidates/conflict@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}
	_, err = executor.PublishArtifactWork(context.Background(), work)
	var artifactErr *domainexecution.ArtifactError
	if !errors.As(err, &artifactErr) {
		t.Fatalf("conflicting target error = %v, want artifact error", err)
	}
}

func publishTestWork() domainexecution.Work {
	return domainexecution.Work{
		OwnerID: "workflow-publish-test", CandidateID: "candidate-publish-test", ArchiveSHA256: publishTestDigest,
		Snapshot: domainexecution.Snapshot{
			Runtime: challenge.RuntimeK8s, Checkpoints: []domainexecution.CheckpointSnapshot{{ID: "ready"}},
			K8s: &domainexecution.K8sRuntimeSnapshot{
				BaseImageDigest: "registry.example/base@" + publishTestDigest, ProfileRevision: "profile-v1", Version: "v1", ManagementTerminalImage: "registry.example/terminal@" + publishTestDigest,
				Resources: domainexecution.K8sResources{
					ControlPlaneCPU: "1", ControlPlaneMemory: "512Mi", ControlPlaneEphemeralStorage: "1Gi",
					WorkloadCPU: "1", WorkloadMemory: "512Mi", WorkloadEphemeralStorage: "1Gi",
					QuotaCPU: "3", QuotaMemory: "3Gi", QuotaEphemeralStorage: "30Gi",
				},
			},
		},
		Attempt: 1, DeadlineAt: time.Now().Add(time.Hour),
		Build: &domainexecution.BuildOutput{Runtime: challenge.RuntimeK8s, OCIReference: "registry.example/build@" + publishTestDigest},
	}
}

type fakeRegistry struct {
	resolved  map[string]string
	copyCalls int
}

func (r *fakeRegistry) PushOCIArchive(context.Context, string, string) error { return nil }

func (r *fakeRegistry) ResolveImmutableReference(_ context.Context, reference string) (string, error) {
	if r.resolved == nil {
		return "", errors.New("not found")
	}
	value, ok := r.resolved[reference]
	if !ok {
		return "", oci.ErrReferenceNotFound
	}
	return value, nil
}

func (r *fakeRegistry) CopyImage(_ context.Context, source, target string) error {
	r.copyCalls++
	_, digest, found := strings.Cut(source, "@")
	if !found {
		return errors.New("source is not immutable")
	}
	if r.resolved == nil {
		r.resolved = make(map[string]string)
	}
	r.resolved[target] = target[:strings.LastIndex(target, ":")] + "@" + digest
	return nil
}

func (r *fakeRegistry) DeleteImage(context.Context, string) error { return nil }
