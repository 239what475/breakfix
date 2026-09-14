package runnableworker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/breakfix/breakfix/internal/domain/runnable"
)

// SourceReader is the only byte ingress used by materialization. Its
// implementation can retrieve an immutable archive from durable storage, but
// is not trusted for identity: ArchiveMaterializer verifies the supplied
// digest before passing bytes to a provider.
type SourceReader interface {
	ReadSource(context.Context, runnable.LeaseCredential, runnable.SourceArchive) ([]byte, error)
}

// ArtifactBuilder materializes provider-specific bytes from a frozen spec and
// verified source archive. The public contract intentionally does not expose
// provider SDK values or product-specific source conventions.
type ArtifactBuilder interface {
	BuildArtifact(context.Context, runnable.RunnableSpec, []byte) (runnable.ArtifactReference, error)
}

type ArchiveMaterializer struct {
	source  SourceReader
	builder ArtifactBuilder
}

func NewArchiveMaterializer(source SourceReader, builder ArtifactBuilder) (*ArchiveMaterializer, error) {
	if source == nil || builder == nil {
		return nil, errors.New("runnable archive materializer requires source reader and artifact builder")
	}
	return &ArchiveMaterializer{source: source, builder: builder}, nil
}

func (m *ArchiveMaterializer) Materialize(ctx context.Context, request runnable.MaterializeRequest) (runnable.ArtifactReference, error) {
	if err := request.Validate(); err != nil {
		return runnable.ArtifactReference{}, err
	}
	archive, err := m.source.ReadSource(ctx, request.Credential, request.Spec.Source)
	if err != nil {
		return runnable.ArtifactReference{}, fmt.Errorf("read runnable source archive: %w", err)
	}
	if archiveDigest(archive) != request.Spec.Source.Digest {
		return runnable.ArtifactReference{}, runnable.NewArtifactFailure("source-archive-digest", "source archive does not match the frozen runnable spec")
	}
	artifact, err := m.builder.BuildArtifact(ctx, request.Spec, archive)
	if err != nil {
		return runnable.ArtifactReference{}, err
	}
	if err := artifact.Validate(); err != nil {
		return runnable.ArtifactReference{}, fmt.Errorf("artifact builder returned an invalid reference: %w", err)
	}
	specDigest, err := request.Spec.Digest()
	if err != nil {
		return runnable.ArtifactReference{}, err
	}
	if artifact.Runtime != request.Spec.RuntimeProfile.Runtime || artifact.BuiltFromSpecDigest != specDigest {
		return runnable.ArtifactReference{}, runnable.NewArtifactFailure("artifact-spec-binding", "artifact builder returned a reference for another runnable spec")
	}
	return artifact, nil
}

func archiveDigest(archive []byte) string {
	sum := sha256.Sum256(archive)
	return "sha256:" + hex.EncodeToString(sum[:])
}
