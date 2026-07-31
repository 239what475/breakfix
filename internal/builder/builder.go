// Package builder implements the fixed, non-executing candidate build stage.
package builder

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/breakfix/breakfix/internal/candidate"
	"github.com/breakfix/breakfix/internal/candidateworker"
	"github.com/breakfix/breakfix/internal/challenge"
	"github.com/breakfix/breakfix/internal/generator"
	"github.com/breakfix/breakfix/internal/incusprovider"
	"github.com/breakfix/breakfix/internal/registry"
)

type NodeImageBuilder interface {
	BuildNodeImage(context.Context, incusprovider.BuildNodeImageRequest) (incusprovider.BuildNodeImageResult, error)
}

type Executor struct {
	client *candidateworker.Client
	node   NodeImageBuilder
	config incusprovider.Config
}

func NewExecutor(client *candidateworker.Client, node NodeImageBuilder, config incusprovider.Config) (*Executor, error) {
	if client == nil {
		return nil, errors.New("builder requires a Server client")
	}
	return &Executor{client: client, node: node, config: config}, nil
}

func (e *Executor) Execute(ctx context.Context, claim candidateworker.Claim) error {
	archive, digest, err := e.client.DownloadCandidateArchive(ctx, claim)
	if err != nil {
		return err
	}
	if digest != claim.Candidate.ArchiveSHA256 || candidate.Digest(archive) != digest {
		return candidateworker.ArtifactFailure("CANDIDATE_ARCHIVE_DIGEST_MISMATCH", "candidate archive digest does not match its immutable revision", nil)
	}
	if _, err := generator.InspectCandidateArchive(archive); err != nil {
		return candidateworker.ArtifactFailure("CANDIDATE_INVALID", err.Error(), nil)
	}
	root, err := os.MkdirTemp("", "breakfix-builder-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root) //nolint:errcheck
	bundle := filepath.Join(root, "challenge")
	if err := os.MkdirAll(bundle, 0o750); err != nil {
		return err
	}
	if err := challenge.ExtractTarGz(bundle, bytes.NewReader(archive)); err != nil {
		return candidateworker.ArtifactFailure("CANDIDATE_ARCHIVE_INVALID", err.Error(), nil)
	}

	switch claim.Candidate.Snapshot.Runtime {
	case challenge.RuntimeNode:
		return e.buildNode(ctx, claim, bundle)
	case challenge.RuntimeK8s:
		return e.buildK8s(ctx, claim, bundle, root)
	default:
		return candidateworker.ArtifactFailure("CANDIDATE_RUNTIME_INVALID", "candidate runtime is unsupported", nil)
	}
}

func (e *Executor) buildNode(ctx context.Context, claim candidateworker.Claim, bundle string) error {
	if e.node == nil {
		return errors.New("node image builder is unavailable")
	}
	files, err := incusprovider.ImageFilesFromDirectory(bundle)
	if err != nil {
		return candidateworker.ArtifactFailure("CANDIDATE_FILES_INVALID", err.Error(), nil)
	}
	result, err := e.node.BuildNodeImage(ctx, incusprovider.BuildNodeImageRequest{
		WorkItemID: claim.Work.Item.ID, Attempt: int64(claim.Work.Item.Attempt),
		Revision: claim.Candidate.ArchiveSHA256, Files: files,
	})
	if err != nil {
		return fmt.Errorf("build stopped Node image: %w", err)
	}
	output := candidate.BuildOutput{
		Runtime: challenge.RuntimeNode,
		Incus: &candidate.IncusBuildReference{
			Project: e.config.BuildProject, WorkItemID: result.WorkItemID, Attempt: result.Attempt,
			InstanceName: result.InstanceName, Alias: result.Alias, Fingerprint: result.Fingerprint,
		},
	}
	return e.client.CompleteBuild(ctx, claim, output, nil)
}

func (e *Executor) buildK8s(ctx context.Context, claim candidateworker.Claim, bundle, root string) error {
	base, baseDigest, err := e.client.DownloadK8sBase(ctx, claim)
	if err != nil {
		return err
	}
	if candidate.Digest(base) != baseDigest {
		return errors.New("trusted K8s base archive digest mismatch")
	}
	basePath := filepath.Join(root, "base.oci.tar")
	outputPath := filepath.Join(root, "candidate.oci.tar")
	if err := os.WriteFile(basePath, base, 0o400); err != nil {
		return err
	}
	if _, err := registry.AppendChallengeLayer(basePath, bundle, outputPath); err != nil {
		return fmt.Errorf("append deterministic K8s challenge layer: %w", err)
	}
	output, err := os.ReadFile(outputPath)
	if err != nil {
		return err
	}
	return e.client.CompleteBuild(ctx, claim, candidate.BuildOutput{
		Runtime: challenge.RuntimeK8s, OCIArchiveSHA256: candidate.Digest(output),
	}, output)
}
