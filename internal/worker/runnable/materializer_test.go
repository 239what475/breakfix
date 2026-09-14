package runnableworker

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/breakfix/breakfix/internal/domain/runnable"
)

func TestArchiveMaterializerVerifiesSourceAndSpecBindings(t *testing.T) {
	spec := validSpec()
	archive := []byte("canonical source archive")
	spec.Source.Digest = archiveDigest(archive)
	builder := &fakeArtifactBuilder{}
	materializer, err := NewArchiveMaterializer(fakeSourceReader{archive: archive}, builder)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := materializer.Materialize(context.Background(), spec)
	if err != nil {
		t.Fatalf("materialize archive: %v", err)
	}
	if !builder.called || artifact.BuiltFromSpecDigest == "" {
		t.Fatalf("builder was not called with a bound artifact: %#v", artifact)
	}
}

func TestArchiveMaterializerRejectsChangedSourceBeforeBuild(t *testing.T) {
	spec := validSpec()
	spec.Source.Digest = archiveDigest([]byte("expected"))
	builder := &fakeArtifactBuilder{}
	materializer, err := NewArchiveMaterializer(fakeSourceReader{archive: []byte("changed")}, builder)
	if err != nil {
		t.Fatal(err)
	}
	_, err = materializer.Materialize(context.Background(), spec)
	var artifactFailure *runnable.ArtifactFailure
	if !errors.As(err, &artifactFailure) || artifactFailure.Code != "source-archive-digest" || builder.called {
		t.Fatalf("expected source digest artifact failure before build, got %#v", err)
	}
}

func TestArchiveMaterializerRejectsArtifactForAnotherSpec(t *testing.T) {
	spec := validSpec()
	archive := []byte("canonical source archive")
	spec.Source.Digest = archiveDigest(archive)
	builder := &fakeArtifactBuilder{overrideSpecDigest: testDigest("e")}
	materializer, err := NewArchiveMaterializer(fakeSourceReader{archive: archive}, builder)
	if err != nil {
		t.Fatal(err)
	}
	_, err = materializer.Materialize(context.Background(), spec)
	var artifactFailure *runnable.ArtifactFailure
	if !errors.As(err, &artifactFailure) || artifactFailure.Code != "artifact-spec-binding" {
		t.Fatalf("expected artifact binding failure, got %#v", err)
	}
}

type fakeSourceReader struct{ archive []byte }

func (r fakeSourceReader) ReadSource(context.Context, runnable.SourceArchive) ([]byte, error) {
	return append([]byte(nil), r.archive...), nil
}

type fakeArtifactBuilder struct {
	called             bool
	overrideSpecDigest string
}

func (b *fakeArtifactBuilder) BuildArtifact(_ context.Context, spec runnable.RunnableSpec, archive []byte) (runnable.ArtifactReference, error) {
	b.called = true
	if len(archive) == 0 {
		return runnable.ArtifactReference{}, errors.New("unexpected empty archive")
	}
	specDigest, err := spec.Digest()
	if err != nil {
		return runnable.ArtifactReference{}, err
	}
	if b.overrideSpecDigest != "" {
		specDigest = b.overrideSpecDigest
	}
	artifactDigest := testDigest("c")
	return runnable.ArtifactReference{
		FormatVersion: runnable.FormatVersion, Runtime: spec.RuntimeProfile.Runtime,
		ProviderReference: "provider://artifacts/example@" + artifactDigest,
		ArtifactDigest:    artifactDigest, BuiltFromSpecDigest: specDigest, BuilderVersion: "builder-01",
	}, nil
}

func TestArchiveDigestUsesFullSHA256Notation(t *testing.T) {
	value := archiveDigest([]byte("source"))
	if !runnable.ValidDigest(value) || strings.TrimPrefix(value, "sha256:") == "" {
		t.Fatalf("archive digest = %q", value)
	}
}
