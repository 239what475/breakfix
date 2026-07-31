package publisher

import (
	"context"
	"errors"
	"testing"
)

type immutableRegistry struct {
	reference string
	requested string
}

func (r *immutableRegistry) PushOCIArchive(context.Context, string, string) error {
	return errors.New("unexpected PushOCIArchive")
}

func (r *immutableRegistry) ResolveImmutableReference(_ context.Context, image string) (string, error) {
	r.requested = image
	return r.reference, nil
}

func (r *immutableRegistry) CopyImage(context.Context, string, string) error {
	return errors.New("unexpected CopyImage")
}

func (r *immutableRegistry) DeleteImage(context.Context, string) error {
	return errors.New("unexpected DeleteImage")
}

func TestResolveImmutablePreservesRegistryReference(t *testing.T) {
	registry := &immutableRegistry{reference: "registry.breakfix.test:5000/candidates/abc@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	executor := &Executor{registry: registry}

	got, err := executor.resolveImmutable(context.Background(), "registry.breakfix.test:5000/candidates/abc:artifact")
	if err != nil {
		t.Fatalf("resolve immutable: %v", err)
	}
	if want := registry.reference; got != want {
		t.Fatalf("immutable reference = %q, want %q", got, want)
	}
	if want := "registry.breakfix.test:5000/candidates/abc:artifact"; registry.requested != want {
		t.Fatalf("Registry target = %q, want %q", registry.requested, want)
	}
}

func TestResolveImmutableRejectsMalformedRegistryReference(t *testing.T) {
	executor := &Executor{registry: &immutableRegistry{reference: "registry.breakfix.test:5000/candidates/abc@sha256:not-a-digest"}}
	if _, err := executor.resolveImmutable(context.Background(), "registry.breakfix.test:5000/candidates/abc:artifact"); err == nil {
		t.Fatal("resolve immutable unexpectedly accepted an invalid Registry reference")
	}
}
