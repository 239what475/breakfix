package runnableprovider

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/breakfix/breakfix/internal/adapter/incus"
	"github.com/breakfix/breakfix/internal/domain/runnable"
)

func TestArtifactBuilderBuildsNodeReferenceBoundToSpec(t *testing.T) {
	spec := testSpec(runnable.RuntimeNode)
	node := &fakeNodeBuilder{result: incus.BuildNodeImageResult{Alias: "breakfix-build", Fingerprint: strings.Repeat("a", 64)}}
	builder, err := NewArtifactBuilder(node, nil, incus.Config{}, "")
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := builder.BuildArtifact(context.Background(), spec, archiveBytes(t))
	if err != nil {
		t.Fatal(err)
	}
	digest, _ := spec.Digest()
	if artifact.BuiltFromSpecDigest != digest || artifact.ArtifactDigest != "sha256:"+strings.Repeat("a", 64) {
		t.Fatalf("artifact binding = %#v", artifact)
	}
	if err := artifact.Validate(); err != nil {
		t.Fatalf("artifact validation: %v", err)
	}
	if node.request.Files[0].Path != "scripts/init.sh" {
		t.Fatalf("provider received files = %#v", node.request.Files)
	}
}

func TestArtifactBuilderRejectsMalformedSourceArchive(t *testing.T) {
	builder, err := NewArtifactBuilder(&fakeNodeBuilder{}, nil, incus.Config{}, "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = builder.BuildArtifact(context.Background(), testSpec(runnable.RuntimeNode), []byte("not gzip"))
	var artifactFailure *runnable.ArtifactFailure
	if err == nil || !errors.As(err, &artifactFailure) || artifactFailure.Code != "source-archive-invalid" {
		t.Fatalf("expected source archive failure, got %v", err)
	}
}

type fakeNodeBuilder struct {
	result  incus.BuildNodeImageResult
	request incus.BuildNodeImageRequest
}

func (f *fakeNodeBuilder) BuildNodeImage(_ context.Context, request incus.BuildNodeImageRequest) (incus.BuildNodeImageResult, error) {
	f.request = request
	if f.result.Fingerprint == "" {
		f.result = incus.BuildNodeImageResult{Alias: "build", Fingerprint: strings.Repeat("b", 64)}
	}
	return f.result, nil
}

func archiveBytes(t *testing.T) []byte {
	t.Helper()
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tarWriter := tar.NewWriter(gz)
	if err := tarWriter.WriteHeader(&tar.Header{Name: "scripts/init.sh", Mode: 0o755, Size: int64(len("#!/bin/sh\n"))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write([]byte("#!/bin/sh\n")); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func testSpec(runtime runnable.Runtime) runnable.RunnableSpec {
	profile := runnable.RuntimeProfile{
		Runtime: runtime, ProfileRevision: "profile-1", BaseImage: "registry.example/base@sha256:" + strings.Repeat("c", 64),
		SoftwareVersions: map[string]string{"runtime": "1"}, Resources: runnable.ResourceLimits{CPU: "1", MemoryBytes: 1, EphemeralBytes: 1, MaxProcesses: 1, MaxConcurrentTasks: 1}, Network: runnable.NetworkPrivate, Topology: "single",
		ExecutionBoundaries: []runnable.ExecutionBoundary{{ID: "node-write", Target: runnable.TargetLocation{Kind: "node", ID: "host"}, Permission: runnable.PermissionReadWrite, Network: runnable.NetworkPrivate, MaxTimeout: 60}, {ID: "node-read", Target: runnable.TargetLocation{Kind: "node", ID: "host"}, Permission: runnable.PermissionReadOnly, Network: runnable.NetworkPrivate, MaxTimeout: 60}},
	}
	return runnable.RunnableSpec{FormatVersion: runnable.FormatVersion, Identity: runnable.ContentIdentity{Kind: "operations", ID: "content-1", Revision: "revision-1"}, RuntimeProfile: profile, Source: runnable.SourceArchive{FormatVersion: runnable.FormatVersion, Reference: "archives/source.tar.gz", Digest: "sha256:" + strings.Repeat("d", 64)}, Initialization: []runnable.ActionSpec{{ID: "initialize", Entrypoint: "scripts/init.sh", Target: runnable.TargetLocation{Kind: "node", ID: "host"}, BoundaryID: "node-write", TimeoutSeconds: 10, ExpectedExitCodes: []int{0}}}, ValidationPlan: runnable.ValidationPlan{FormatVersion: runnable.FormatVersion, Phases: []runnable.ValidationPhase{{ID: "observe", TimeoutSeconds: 10, Execution: runnable.PhaseSequential, Assertions: []runnable.AssertionSpec{{ID: "ready", Entrypoint: "scripts/init.sh", Target: runnable.TargetLocation{Kind: "node", ID: "host"}, BoundaryID: "node-read", TimeoutSeconds: 10}}}}}, LifecyclePolicy: runnable.LifecyclePolicy{CreateTimeoutSeconds: 10, ResetTimeoutSeconds: 10, StopTimeoutSeconds: 10, ReapTimeoutSeconds: 10, IdleTTLSeconds: 10, MaxLifetimeSeconds: 10}}
}
