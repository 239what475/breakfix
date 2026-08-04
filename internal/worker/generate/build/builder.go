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

	"github.com/breakfix/breakfix/internal/adapter/incus"
	"github.com/breakfix/breakfix/internal/adapter/oci"
	app "github.com/breakfix/breakfix/internal/application/generation"
	"github.com/breakfix/breakfix/internal/content/candidate"
	"github.com/breakfix/breakfix/internal/content/challenge"
	domainexecution "github.com/breakfix/breakfix/internal/domain/execution"
	"github.com/breakfix/breakfix/internal/domain/generation"
)

type NodeImageBuilder interface {
	BuildNodeImage(context.Context, incus.BuildNodeImageRequest) (incus.BuildNodeImageResult, error)
}

type Executor struct {
	node   NodeImageBuilder
	config incus.Config
}

func NewExecutor(node NodeImageBuilder, config incus.Config) *Executor {
	return &Executor{node: node, config: config}
}

// Execute preserves the GenerationWorker adapter while the implementation
// itself consumes the neutral execution contract below.
func (e *Executor) Execute(ctx context.Context, value generation.Execution, archive, base []byte) (generation.BuildResult, error) {
	if !value.Valid() || value.Claim.Workflow.State != generation.StateBuilding || value.Context.Candidate == nil || value.Claim.Workflow.DeadlineAt == nil {
		return generation.BuildResult{}, errors.New("builder requires a Building generation workflow with a candidate")
	}
	work := domainexecution.Work{
		OwnerID: value.Claim.Workflow.ID, CandidateID: value.Context.Candidate.ID,
		ArchiveSHA256: value.Context.Candidate.ArchiveSHA256, Snapshot: value.Context.Candidate.Snapshot,
		Attempt: int64(value.Claim.StateAttempt + 1), DeadlineAt: value.Claim.Workflow.DeadlineAt.UTC(),
	}
	output, built, err := e.ExecuteWork(ctx, work, archive, base)
	if err != nil {
		return generation.BuildResult{}, err
	}
	return generation.BuildResult{Output: output, Archive: built}, nil
}

// ExecuteWork builds one portable candidate without assuming who owns the
// execution. The caller persists output and controls retries/leases.
func (e *Executor) ExecuteWork(ctx context.Context, work domainexecution.Work, archive, base []byte) (domainexecution.BuildOutput, []byte, error) {
	if err := work.Validate(); err != nil {
		return domainexecution.BuildOutput{}, nil, fmt.Errorf("builder execution work: %w", err)
	}
	if candidate.Digest(archive) != work.ArchiveSHA256 {
		return domainexecution.BuildOutput{}, nil, domainexecution.NewArtifactError("CANDIDATE_ARCHIVE_DIGEST_MISMATCH", "candidate archive digest does not match its immutable revision")
	}
	if _, err := app.InspectCandidateArchive(archive); err != nil {
		return domainexecution.BuildOutput{}, nil, domainexecution.NewArtifactError("CANDIDATE_INVALID", err.Error())
	}
	root, err := os.MkdirTemp("", "breakfix-builder-")
	if err != nil {
		return domainexecution.BuildOutput{}, nil, err
	}
	defer func() { _ = os.RemoveAll(root) }()
	bundle := filepath.Join(root, "challenge")
	if err := os.MkdirAll(bundle, 0o750); err != nil {
		return domainexecution.BuildOutput{}, nil, err
	}
	if err := challenge.ExtractTarGz(bundle, bytes.NewReader(archive)); err != nil {
		return domainexecution.BuildOutput{}, nil, domainexecution.NewArtifactError("CANDIDATE_ARCHIVE_INVALID", err.Error())
	}

	switch work.Snapshot.Runtime {
	case challenge.RuntimeNode:
		output, err := e.buildNode(ctx, work, bundle)
		return output, nil, err
	case challenge.RuntimeK8s:
		output, built, err := e.buildK8s(bundle, root, base)
		return output, built, err
	default:
		return domainexecution.BuildOutput{}, nil, domainexecution.NewArtifactError("CANDIDATE_RUNTIME_INVALID", "candidate runtime is unsupported")
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
		WorkflowID: work.OwnerID,
		Attempt:    work.Attempt,
		Revision:   work.ArchiveSHA256,
		Files:      files,
	})
	if err != nil {
		return domainexecution.BuildOutput{}, fmt.Errorf("build stopped Node image: %w", err)
	}
	output := domainexecution.BuildOutput{
		Runtime: challenge.RuntimeNode,
		Incus: &domainexecution.IncusBuildReference{
			Project:      e.config.BuildProject,
			WorkflowID:   result.WorkflowID,
			Attempt:      result.Attempt,
			InstanceName: result.InstanceName,
			Alias:        result.Alias,
			Fingerprint:  result.Fingerprint,
		},
	}
	return output, nil
}

func (e *Executor) buildK8s(bundle, root string, base []byte) (domainexecution.BuildOutput, []byte, error) {
	if len(base) == 0 {
		return domainexecution.BuildOutput{}, nil, errors.New("trusted K8s base archive is unavailable")
	}
	basePath := filepath.Join(root, "base.oci.tar")
	outputPath := filepath.Join(root, "candidate.oci.tar")
	if err := os.WriteFile(basePath, base, 0o400); err != nil {
		return domainexecution.BuildOutput{}, nil, err
	}
	if _, err := oci.AppendChallengeLayer(basePath, bundle, outputPath); err != nil {
		return domainexecution.BuildOutput{}, nil, fmt.Errorf("append deterministic K8s challenge layer: %w", err)
	}
	archive, err := os.ReadFile(outputPath)
	if err != nil {
		return domainexecution.BuildOutput{}, nil, err
	}
	return domainexecution.BuildOutput{Runtime: challenge.RuntimeK8s, OCIArchiveSHA256: candidate.Digest(archive)}, archive, nil
}
