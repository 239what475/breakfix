// Package build builds an immutable candidate for one GenerationWorkflow
// phase. It owns no queue, lease, or Server mutation.
package build

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/breakfix/breakfix/internal/adapter/incus"
	"github.com/breakfix/breakfix/internal/adapter/oci"
	app "github.com/breakfix/breakfix/internal/application/generation"
	"github.com/breakfix/breakfix/internal/content/candidate"
	"github.com/breakfix/breakfix/internal/content/challenge"
	domainexecution "github.com/breakfix/breakfix/internal/domain/execution"
)

type NodeImageBuilder interface {
	BuildNodeImage(context.Context, incus.BuildNodeImageRequest) (incus.BuildNodeImageResult, error)
}

type Registry interface {
	PullOCIArchive(context.Context, string, string) error
	PushOCIArchive(context.Context, string, string) error
	ResolveImmutableReference(context.Context, string) (string, error)
}

type Executor struct {
	node               NodeImageBuilder
	registry           Registry
	config             incus.Config
	registryRepository string
}

func NewExecutor(node NodeImageBuilder, registry Registry, config incus.Config, registryRepository string) (*Executor, error) {
	if registry == nil || strings.TrimSpace(registryRepository) == "" {
		return nil, errors.New("builder requires Registry client and Registry repository")
	}
	return &Executor{
		node: node, registry: registry, config: config, registryRepository: strings.TrimRight(strings.TrimSpace(registryRepository), "/"),
	}, nil
}

// ExecuteWork builds one portable candidate without assuming who owns the
// execution. The caller persists output and controls retries/leases. K8s build
// output is written directly to a build-scoped immutable Registry artifact;
// no output archive crosses the Server boundary.
func (e *Executor) ExecuteWork(ctx context.Context, work domainexecution.Work, archive []byte) (domainexecution.BuildOutput, error) {
	if err := work.Validate(); err != nil {
		return domainexecution.BuildOutput{}, fmt.Errorf("builder execution work: %w", err)
	}
	if candidate.Digest(archive) != work.ArchiveSHA256 {
		return domainexecution.BuildOutput{}, domainexecution.NewArtifactError("CANDIDATE_ARCHIVE_DIGEST_MISMATCH", "candidate archive digest does not match its immutable revision")
	}
	if _, err := app.InspectCandidateArchive(archive); err != nil {
		return domainexecution.BuildOutput{}, domainexecution.NewArtifactError("CANDIDATE_INVALID", err.Error())
	}
	root, err := os.MkdirTemp("", "breakfix-builder-")
	if err != nil {
		return domainexecution.BuildOutput{}, err
	}
	defer func() { _ = os.RemoveAll(root) }()
	bundle := filepath.Join(root, "challenge")
	if err := os.MkdirAll(bundle, 0o750); err != nil {
		return domainexecution.BuildOutput{}, err
	}
	if err := challenge.ExtractTarGz(bundle, bytes.NewReader(archive)); err != nil {
		return domainexecution.BuildOutput{}, domainexecution.NewArtifactError("CANDIDATE_ARCHIVE_INVALID", err.Error())
	}

	switch work.Snapshot.Runtime {
	case challenge.RuntimeNode:
		output, err := e.buildNode(ctx, work, bundle)
		return output, err
	case challenge.RuntimeK8s:
		return e.buildK8s(ctx, work, bundle, root)
	default:
		return domainexecution.BuildOutput{}, domainexecution.NewArtifactError("CANDIDATE_RUNTIME_INVALID", "candidate runtime is unsupported")
	}
}

func (e *Executor) buildNode(ctx context.Context, work domainexecution.Work, bundle string) (domainexecution.BuildOutput, error) {
	if e.node == nil {
		return domainexecution.BuildOutput{}, errors.New("node image builder is unavailable")
	}
	files, err := incus.ImageFilesFromDirectory(bundle)
	if err != nil {
		return domainexecution.BuildOutput{}, domainexecution.NewArtifactError("CANDIDATE_FILES_INVALID", err.Error())
	}
	result, err := e.node.BuildNodeImage(ctx, incus.BuildNodeImageRequest{
		WorkflowID: work.OwnerID, CandidateRevisionID: work.CandidateID,
		Attempt: work.Attempt, Revision: work.ArchiveSHA256, Files: files,
	})
	if err != nil {
		return domainexecution.BuildOutput{}, fmt.Errorf("build stopped Node image: %w", err)
	}
	output := domainexecution.BuildOutput{
		Runtime: challenge.RuntimeNode,
		Incus: &domainexecution.IncusBuildReference{
			Project: e.config.BuildProject, WorkflowID: result.WorkflowID, CandidateRevisionID: result.CandidateRevisionID,
			Attempt: result.Attempt, InstanceName: result.InstanceName, Alias: result.Alias, Fingerprint: result.Fingerprint,
		},
	}
	return output, nil
}

func (e *Executor) buildK8s(ctx context.Context, work domainexecution.Work, bundle, root string) (domainexecution.BuildOutput, error) {
	if e.registry == nil || work.Snapshot.K8s == nil {
		return domainexecution.BuildOutput{}, errors.New("trusted K8s base artifact is unavailable")
	}
	basePath := filepath.Join(root, "base.oci.tar")
	outputPath := filepath.Join(root, "candidate.oci.tar")
	if err := e.registry.PullOCIArchive(ctx, work.Snapshot.K8s.BaseImageDigest, basePath); err != nil {
		return domainexecution.BuildOutput{}, fmt.Errorf("pull trusted K8s base image: %w", err)
	}
	manifestDigest, err := oci.AppendChallengeLayer(basePath, bundle, outputPath)
	if err != nil {
		return domainexecution.BuildOutput{}, fmt.Errorf("append deterministic K8s challenge layer: %w", err)
	}
	if err := oci.ValidateOCIArchive(outputPath); err != nil {
		return domainexecution.BuildOutput{}, fmt.Errorf("validate K8s build OCI archive: %w", err)
	}
	target, err := candidate.BuildOCIImageReference(e.registryRepository, work.OwnerID, work.CandidateID, work.Attempt)
	if err != nil {
		return domainexecution.BuildOutput{}, err
	}
	if existing, resolveErr := e.registry.ResolveImmutableReference(ctx, target); resolveErr == nil {
		if digest, digestErr := candidate.OCIDigest(existing); digestErr != nil || digest != manifestDigest {
			return domainexecution.BuildOutput{}, domainexecution.NewArtifactError("BUILD_ARTIFACT_CONFLICT", "existing K8s build artifact does not match this immutable action")
		}
		return domainexecution.BuildOutput{Runtime: challenge.RuntimeK8s, OCIReference: existing}, nil
	} else if !errors.Is(resolveErr, oci.ErrReferenceNotFound) {
		return domainexecution.BuildOutput{}, fmt.Errorf("resolve existing K8s build artifact: %w", resolveErr)
	}
	if err := e.registry.PushOCIArchive(ctx, target, outputPath); err != nil {
		return domainexecution.BuildOutput{}, fmt.Errorf("publish K8s build artifact: %w", err)
	}
	immutable, err := e.registry.ResolveImmutableReference(ctx, target)
	if err != nil {
		return domainexecution.BuildOutput{}, fmt.Errorf("resolve K8s build artifact: %w", err)
	}
	if digest, digestErr := candidate.OCIDigest(immutable); digestErr != nil || digest != manifestDigest {
		return domainexecution.BuildOutput{}, domainexecution.NewArtifactError("BUILD_ARTIFACT_CONFLICT", "published K8s build artifact digest does not match this immutable action")
	}
	return domainexecution.BuildOutput{Runtime: challenge.RuntimeK8s, OCIReference: immutable}, nil
}
