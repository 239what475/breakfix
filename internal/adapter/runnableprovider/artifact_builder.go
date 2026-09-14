// Package runnableprovider adapts provider SDK clients to the public runnable
// contract. It deliberately accepts only a frozen RunnableSpec and source
// bytes; scheduling, leases, and product publication remain outside.
package runnableprovider

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/breakfix/breakfix/internal/adapter/incus"
	"github.com/breakfix/breakfix/internal/adapter/oci"
	"github.com/breakfix/breakfix/internal/domain/runnable"
)

// NodeImageBuilder is the minimal Incus capability needed for materializing a
// Node artifact. The provider adapter never exposes the SDK to domain code.
type NodeImageBuilder interface {
	BuildNodeImage(context.Context, incus.BuildNodeImageRequest) (incus.BuildNodeImageResult, error)
}

// Registry is the minimal OCI capability needed for materializing a K8s
// artifact. References are resolved to immutable digests before returning.
type Registry interface {
	PullOCIArchive(context.Context, string, string) error
	PushOCIArchive(context.Context, string, string) error
	ResolveImmutableReference(context.Context, string) (string, error)
}

const builderVersion = "runnable-provider-v1"

type ArtifactBuilder struct {
	node               NodeImageBuilder
	registry           Registry
	incusConfig        incus.Config
	registryRepository string
}

func NewArtifactBuilder(node NodeImageBuilder, registry Registry, incusConfig incus.Config, registryRepository string) (*ArtifactBuilder, error) {
	if node == nil && registry == nil {
		return nil, errors.New("runnable artifact builder requires a Node or Registry provider")
	}
	if strings.TrimSpace(registryRepository) == "" && registry != nil {
		return nil, errors.New("runnable artifact builder requires a Registry repository")
	}
	return &ArtifactBuilder{
		node: node, registry: registry, incusConfig: incusConfig,
		registryRepository: strings.TrimRight(strings.TrimSpace(registryRepository), "/"),
	}, nil
}

// BuildArtifact materializes a provider artifact from a frozen source
// archive. The source format is the public tar.gz bundle; no product-specific
// archive inspection is performed here.
func (b *ArtifactBuilder) BuildArtifact(ctx context.Context, spec runnable.RunnableSpec, archive []byte) (runnable.ArtifactReference, error) {
	if b == nil {
		return runnable.ArtifactReference{}, errors.New("runnable artifact builder is not configured")
	}
	if err := spec.Validate(); err != nil {
		return runnable.ArtifactReference{}, err
	}
	if len(archive) == 0 {
		return runnable.ArtifactReference{}, runnable.NewArtifactFailure("source-archive-empty", "source archive is empty")
	}
	specDigest, err := spec.Digest()
	if err != nil {
		return runnable.ArtifactReference{}, err
	}
	root, err := os.MkdirTemp("", "breakfix-runnable-build-")
	if err != nil {
		return runnable.ArtifactReference{}, err
	}
	defer func() { _ = os.RemoveAll(root) }()
	bundle := filepath.Join(root, "scenario")
	if err := os.MkdirAll(bundle, 0o750); err != nil {
		return runnable.ArtifactReference{}, err
	}
	if err := extractSourceArchive(bundle, bytes.NewReader(archive)); err != nil {
		return runnable.ArtifactReference{}, runnable.NewArtifactFailure("source-archive-invalid", err.Error())
	}

	switch spec.RuntimeProfile.Runtime {
	case runnable.RuntimeNode:
		return b.buildNode(ctx, spec, specDigest, bundle)
	case runnable.RuntimeK8s:
		return b.buildK8s(ctx, spec, specDigest, bundle, root)
	default:
		return runnable.ArtifactReference{}, runnable.NewArtifactFailure("runtime-unsupported", "runnable runtime is unsupported")
	}
}

func (b *ArtifactBuilder) buildNode(ctx context.Context, spec runnable.RunnableSpec, specDigest, bundle string) (runnable.ArtifactReference, error) {
	if b.node == nil {
		return runnable.ArtifactReference{}, errors.New("Node artifact provider is unavailable")
	}
	files, err := incus.ImageFilesFromDirectory(bundle)
	if err != nil {
		return runnable.ArtifactReference{}, runnable.NewArtifactFailure("source-files-invalid", err.Error())
	}
	result, err := b.node.BuildNodeImage(ctx, incus.BuildNodeImageRequest{
		WorkflowID:          spec.Identity.Kind + "-" + spec.Identity.Revision,
		CandidateRevisionID: spec.Identity.ID,
		Attempt:             1,
		Revision:            specDigest,
		Files:               files,
	})
	if err != nil {
		return runnable.ArtifactReference{}, fmt.Errorf("build Node runnable artifact: %w", err)
	}
	if strings.TrimSpace(result.Alias) == "" || len(strings.TrimSpace(result.Fingerprint)) != 64 {
		return runnable.ArtifactReference{}, runnable.NewArtifactFailure("provider-reference-invalid", "Node provider returned an incomplete image identity")
	}
	artifactDigest := "sha256:" + strings.ToLower(strings.TrimSpace(result.Fingerprint))
	return runnable.ArtifactReference{
		FormatVersion: runnable.FormatVersion, Runtime: runnable.RuntimeNode,
		ProviderReference: "incus://" + result.Alias + "@" + artifactDigest,
		ArtifactDigest:    artifactDigest, BuiltFromSpecDigest: specDigest, BuilderVersion: builderVersion,
	}, nil
}

func (b *ArtifactBuilder) buildK8s(ctx context.Context, spec runnable.RunnableSpec, specDigest, bundle, root string) (runnable.ArtifactReference, error) {
	if b.registry == nil {
		return runnable.ArtifactReference{}, errors.New("K8s Registry provider is unavailable")
	}
	base := strings.TrimSpace(spec.RuntimeProfile.BaseImage)
	if base == "" {
		return runnable.ArtifactReference{}, runnable.NewArtifactFailure("base-image-missing", "K8s runtime profile has no base image")
	}
	basePath := filepath.Join(root, "base.oci.tar")
	outputPath := filepath.Join(root, "runnable.oci.tar")
	if err := b.registry.PullOCIArchive(ctx, base, basePath); err != nil {
		return runnable.ArtifactReference{}, fmt.Errorf("pull K8s runnable base image: %w", err)
	}
	manifestDigest, err := oci.AppendScenarioLayer(basePath, bundle, outputPath)
	if err != nil {
		return runnable.ArtifactReference{}, runnable.NewArtifactFailure("oci-materialization", err.Error())
	}
	if err := oci.ValidateOCIArchive(outputPath); err != nil {
		return runnable.ArtifactReference{}, runnable.NewArtifactFailure("oci-materialization", err.Error())
	}
	// The repository is derived solely from immutable content identity. A
	// retry or lease takeover therefore reaches the same provider resource.
	target := buildOCIReference(b.registryRepository, spec.Identity, specDigest)
	immutable, resolveErr := b.registry.ResolveImmutableReference(ctx, target)
	if resolveErr == nil {
		digest, digestErr := ociDigest(immutable)
		if digestErr != nil || digest != manifestDigest {
			return runnable.ArtifactReference{}, runnable.NewArtifactFailure("artifact-conflict", "existing K8s artifact does not match this runnable spec")
		}
		return runnable.ArtifactReference{FormatVersion: runnable.FormatVersion, Runtime: runnable.RuntimeK8s, ProviderReference: immutable, ArtifactDigest: digest, BuiltFromSpecDigest: specDigest, BuilderVersion: builderVersion}, nil
	}
	if !errors.Is(resolveErr, oci.ErrReferenceNotFound) {
		return runnable.ArtifactReference{}, fmt.Errorf("resolve K8s runnable artifact: %w", resolveErr)
	}
	if err := b.registry.PushOCIArchive(ctx, target, outputPath); err != nil {
		return runnable.ArtifactReference{}, fmt.Errorf("publish K8s runnable artifact: %w", err)
	}
	immutable, err = b.registry.ResolveImmutableReference(ctx, target)
	if err != nil {
		return runnable.ArtifactReference{}, fmt.Errorf("resolve published K8s runnable artifact: %w", err)
	}
	digest, err := ociDigest(immutable)
	if err != nil || digest != manifestDigest {
		return runnable.ArtifactReference{}, runnable.NewArtifactFailure("artifact-conflict", "published K8s artifact does not match this runnable spec")
	}
	return runnable.ArtifactReference{FormatVersion: runnable.FormatVersion, Runtime: runnable.RuntimeK8s, ProviderReference: immutable, ArtifactDigest: digest, BuiltFromSpecDigest: specDigest, BuilderVersion: builderVersion}, nil
}

// extractSourceArchive owns the public source archive format. It rejects
// links, special files, duplicate paths, and paths outside destination so no
// provider operates on unchecked product-owned archive helpers.
func extractSourceArchive(destination string, source io.Reader) error {
	gzipReader, err := gzip.NewReader(source)
	if err != nil {
		return fmt.Errorf("open source archive gzip stream: %w", err)
	}
	defer func() { _ = gzipReader.Close() }()
	tarReader := tar.NewReader(gzipReader)
	seen := make(map[string]struct{})
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read source archive entry: %w", err)
		}
		name := filepath.Clean(header.Name)
		if name == "." || name == "" {
			continue
		}
		if filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
			return fmt.Errorf("source archive path %q escapes its root", header.Name)
		}
		if _, exists := seen[name]; exists {
			return fmt.Errorf("source archive repeats path %q", header.Name)
		}
		seen[name] = struct{}{}
		target := filepath.Join(destination, name)
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o750); err != nil {
				return fmt.Errorf("create source archive directory: %w", err)
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
				return fmt.Errorf("create source archive parent: %w", err)
			}
			file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, os.FileMode(header.Mode&0o777))
			if err != nil {
				return fmt.Errorf("create source archive file: %w", err)
			}
			_, copyErr := io.Copy(file, tarReader)
			closeErr := file.Close()
			if copyErr != nil {
				return fmt.Errorf("write source archive file: %w", copyErr)
			}
			if closeErr != nil {
				return fmt.Errorf("close source archive file: %w", closeErr)
			}
		default:
			return fmt.Errorf("source archive has unsupported entry %q", header.Name)
		}
	}
}

// buildOCIReference is a stable, opaque provider resource name. The source
// content identity and exact spec digest together fence create-or-get across
// retries without importing a content module's naming policy.
func buildOCIReference(repository string, identity runnable.ContentIdentity, specDigest string) string {
	sum := sha256.Sum256([]byte(identity.Kind + "\x00" + identity.ID + "\x00" + identity.Revision + "\x00" + specDigest))
	return strings.TrimRight(repository, "/") + "/runnable-builds/" + hex.EncodeToString(sum[:12]) + ":artifact"
}

func ociDigest(reference string) (string, error) {
	_, digest, found := strings.Cut(strings.TrimSpace(reference), "@")
	if !found || !runnable.ValidDigest(digest) {
		return "", errors.New("provider returned an invalid immutable OCI reference")
	}
	return digest, nil
}
