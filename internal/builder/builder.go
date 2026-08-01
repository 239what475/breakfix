// Package builder builds an immutable candidate for one GenerationWorkflow
// phase. It owns no queue, lease, or Server mutation.
package builder

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/breakfix/breakfix/internal/adapter/incus"
	"github.com/breakfix/breakfix/internal/adapter/oci"
	"github.com/breakfix/breakfix/internal/candidate"
	"github.com/breakfix/breakfix/internal/challenge"
	"github.com/breakfix/breakfix/internal/domain/generation"
	"github.com/breakfix/breakfix/internal/generator"
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

// Execute performs only the Building phase. The caller owns archive retrieval
// and reports the returned output through GenerationWorkflow's typed phase API.
func (e *Executor) Execute(ctx context.Context, execution generation.Execution, archive, base []byte) (generation.BuildResult, error) {
	if !execution.Valid() || execution.Claim.Workflow.State != generation.StateBuilding || execution.Context.Candidate == nil {
		return generation.BuildResult{}, errors.New("builder requires a Building generation workflow with a candidate")
	}
	view := execution.Context.Candidate
	if candidate.Digest(archive) != view.ArchiveSHA256 {
		return generation.BuildResult{}, generation.NewArtifactError("CANDIDATE_ARCHIVE_DIGEST_MISMATCH", "candidate archive digest does not match its immutable revision")
	}
	if _, err := generator.InspectCandidateArchive(archive); err != nil {
		return generation.BuildResult{}, generation.NewArtifactError("CANDIDATE_INVALID", err.Error())
	}
	root, err := os.MkdirTemp("", "breakfix-builder-")
	if err != nil {
		return generation.BuildResult{}, err
	}
	defer func() { _ = os.RemoveAll(root) }()
	bundle := filepath.Join(root, "challenge")
	if err := os.MkdirAll(bundle, 0o750); err != nil {
		return generation.BuildResult{}, err
	}
	if err := challenge.ExtractTarGz(bundle, bytes.NewReader(archive)); err != nil {
		return generation.BuildResult{}, generation.NewArtifactError("CANDIDATE_ARCHIVE_INVALID", err.Error())
	}

	switch view.Snapshot.Runtime {
	case challenge.RuntimeNode:
		return e.buildNode(ctx, execution, bundle)
	case challenge.RuntimeK8s:
		return e.buildK8s(bundle, root, base)
	default:
		return generation.BuildResult{}, generation.NewArtifactError("CANDIDATE_RUNTIME_INVALID", "candidate runtime is unsupported")
	}
}

func (e *Executor) buildNode(ctx context.Context, execution generation.Execution, bundle string) (generation.BuildResult, error) {
	if e.node == nil {
		return generation.BuildResult{}, errors.New("node image builder is unavailable")
	}
	files, err := incus.ImageFilesFromDirectory(bundle)
	if err != nil {
		return generation.BuildResult{}, generation.NewArtifactError("CANDIDATE_FILES_INVALID", err.Error())
	}
	view := execution.Context.Candidate
	attempt := int64(execution.Claim.StateAttempt + 1)
	result, err := e.node.BuildNodeImage(ctx, incus.BuildNodeImageRequest{
		WorkflowID: execution.Claim.Workflow.ID,
		Attempt:    attempt,
		Revision:   view.ArchiveSHA256,
		Files:      files,
	})
	if err != nil {
		return generation.BuildResult{}, fmt.Errorf("build stopped Node image: %w", err)
	}
	output := generation.BuildOutput{
		Runtime: challenge.RuntimeNode,
		Incus: &generation.IncusBuildReference{
			Project:      e.config.BuildProject,
			WorkflowID:   result.WorkflowID,
			Attempt:      result.Attempt,
			InstanceName: result.InstanceName,
			Alias:        result.Alias,
			Fingerprint:  result.Fingerprint,
		},
	}
	return generation.BuildResult{Output: output}, nil
}

func (e *Executor) buildK8s(bundle, root string, base []byte) (generation.BuildResult, error) {
	if len(base) == 0 {
		return generation.BuildResult{}, errors.New("trusted K8s base archive is unavailable")
	}
	basePath := filepath.Join(root, "base.oci.tar")
	outputPath := filepath.Join(root, "candidate.oci.tar")
	if err := os.WriteFile(basePath, base, 0o400); err != nil {
		return generation.BuildResult{}, err
	}
	if _, err := oci.AppendChallengeLayer(basePath, bundle, outputPath); err != nil {
		return generation.BuildResult{}, fmt.Errorf("append deterministic K8s challenge layer: %w", err)
	}
	archive, err := os.ReadFile(outputPath)
	if err != nil {
		return generation.BuildResult{}, err
	}
	return generation.BuildResult{Output: generation.BuildOutput{
		Runtime: challenge.RuntimeK8s, OCIArchiveSHA256: candidate.Digest(archive),
	}, Archive: archive}, nil
}
