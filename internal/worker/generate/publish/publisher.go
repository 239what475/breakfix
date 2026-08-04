// Package publish publishes and cleans immutable candidate artifacts for a
// GenerationWorkflow phase. It does not schedule work or mutate Server state.
package publish

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/breakfix/breakfix/internal/adapter/incus"
	"github.com/breakfix/breakfix/internal/adapter/oci"
	"github.com/breakfix/breakfix/internal/content/candidate"
	"github.com/breakfix/breakfix/internal/content/challenge"
	domainexecution "github.com/breakfix/breakfix/internal/domain/execution"
	"github.com/breakfix/breakfix/internal/domain/generation"
)

type Registry interface {
	PushOCIArchive(context.Context, string, string) error
	ResolveImmutableReference(context.Context, string) (string, error)
	CopyImage(context.Context, string, string) error
	DeleteImage(context.Context, string) error
}

type NodeImagePublisher interface {
	PublishNodeImage(context.Context, incus.PublishNodeImageRequest) (incus.PublishNodeImageResult, error)
	PublishChallengeNodeImage(context.Context, incus.PublishChallengeNodeImageRequest) (incus.PublishNodeImageResult, error)
	DeleteCandidateNodeImage(context.Context, string, string) error
	DeleteBuildNodeImageAttempt(context.Context, string, int64) error
	DeleteChallengeNodeImage(context.Context, string, string) error
}

type Executor struct {
	registry           Registry
	node               NodeImagePublisher
	registryRepository string
}

func NewExecutor(registryClient Registry, node NodeImagePublisher, registryRepository string) (*Executor, error) {
	if registryClient == nil || strings.TrimSpace(registryRepository) == "" {
		return nil, errors.New("publisher requires Registry client and Registry repository")
	}
	return &Executor{registry: registryClient, node: node, registryRepository: strings.TrimRight(strings.TrimSpace(registryRepository), "/")}, nil
}

// DiscardCandidate removes external artifacts for a candidate that will no
// longer advance through this workflow. It is called before the next Generator
// run, while the failed or superseded candidate is still durably referenced by
// GenerationWorkflow, so a retry can repeat the deletion safely.
func (e *Executor) DiscardCandidate(ctx context.Context, execution generation.Execution) error {
	if !execution.Valid() || execution.Claim.Workflow.State != generation.StateGenerating || execution.Context.Candidate == nil {
		return errors.New("candidate discard requires a Generating workflow with a candidate")
	}
	return e.discardCandidate(ctx, execution.Claim.Workflow.ID, *execution.Context.Candidate)
}

func (e *Executor) PublishArtifact(ctx context.Context, value generation.Execution, buildArchive []byte) (generation.ArtifactReference, error) {
	if !value.Valid() || value.Claim.Workflow.State != generation.StateArtifactPublishing || value.Context.Candidate == nil || value.Claim.Workflow.DeadlineAt == nil {
		return generation.ArtifactReference{}, errors.New("artifact publication requires an ArtifactPublishing workflow with a candidate")
	}
	work, err := workFromGeneration(value)
	if err != nil {
		return generation.ArtifactReference{}, err
	}
	return e.PublishArtifactWork(ctx, work, buildArchive)
}

// PublishArtifactWork publishes an immutable staging artifact for a neutral
// execution owner. It deliberately does not perform any workflow transition.
func (e *Executor) PublishArtifactWork(ctx context.Context, work domainexecution.Work, buildArchive []byte) (domainexecution.ArtifactReference, error) {
	if err := work.Validate(); err != nil {
		return domainexecution.ArtifactReference{}, fmt.Errorf("artifact publication execution work: %w", err)
	}
	if work.Build == nil {
		return generation.ArtifactReference{}, errors.New("candidate has no build output")
	}
	switch work.Snapshot.Runtime {
	case challenge.RuntimeK8s:
		if candidate.Digest(buildArchive) != work.Build.OCIArchiveSHA256 {
			return domainexecution.ArtifactReference{}, errors.New("candidate OCI archive does not match the recorded build output")
		}
		root, err := os.MkdirTemp("", "breakfix-publisher-")
		if err != nil {
			return domainexecution.ArtifactReference{}, err
		}
		defer func() { _ = os.RemoveAll(root) }()
		archivePath := filepath.Join(root, "candidate.oci.tar")
		if err := os.WriteFile(archivePath, buildArchive, 0o400); err != nil {
			return domainexecution.ArtifactReference{}, err
		}
		if err := oci.ValidateOCIArchive(archivePath); err != nil {
			return domainexecution.ArtifactReference{}, fmt.Errorf("validate Server build archive: %w", err)
		}
		target, err := e.candidateImage(work.CandidateID)
		if err != nil {
			return domainexecution.ArtifactReference{}, err
		}
		if err := e.registry.PushOCIArchive(ctx, target, archivePath); err != nil {
			return domainexecution.ArtifactReference{}, fmt.Errorf("publish candidate OCI image: %w", err)
		}
		immutable, err := e.resolveImmutable(ctx, target)
		if err != nil {
			return domainexecution.ArtifactReference{}, err
		}
		return domainexecution.ArtifactReference{Runtime: challenge.RuntimeK8s, OCIReference: immutable}, nil

	case challenge.RuntimeNode:
		if e.node == nil {
			return domainexecution.ArtifactReference{}, errors.New("node image publisher is unavailable")
		}
		build, err := nodeBuildResult(work.Build)
		if err != nil {
			return domainexecution.ArtifactReference{}, err
		}
		published, err := e.node.PublishNodeImage(ctx, incus.PublishNodeImageRequest{
			CandidateRevisionID: work.CandidateID,
			Revision:            work.ArchiveSHA256,
			Build:               build,
		})
		if err != nil {
			return domainexecution.ArtifactReference{}, fmt.Errorf("publish candidate Node image: %w", err)
		}
		return domainexecution.ArtifactReference{Runtime: challenge.RuntimeNode, IncusAlias: published.Alias, IncusFingerprint: published.Fingerprint}, nil
	default:
		return domainexecution.ArtifactReference{}, errors.New("candidate runtime is unsupported")
	}
}

func (e *Executor) PublishChallenge(ctx context.Context, value generation.Execution) (generation.ArtifactReference, error) {
	if !value.Valid() || value.Claim.Workflow.State != generation.StateChallengePublishing || value.Context.Candidate == nil || value.Claim.Workflow.DeadlineAt == nil {
		return generation.ArtifactReference{}, errors.New("challenge publication requires a ChallengePublishing workflow with a candidate")
	}
	if value.Context.Candidate.Publication == nil {
		return generation.ArtifactReference{}, errors.New("candidate challenge publication has no intent or verified artifact")
	}
	work, err := workFromGeneration(value)
	if err != nil {
		return generation.ArtifactReference{}, err
	}
	return e.PublishChallengeWork(ctx, work, value.Context.Candidate.Publication.ChallengeID)
}

// PublishChallengeWork promotes a verified staging artifact to a final,
// challenge-scoped artifact. The caller owns materialization and visibility.
func (e *Executor) PublishChallengeWork(ctx context.Context, work domainexecution.Work, challengeID string) (domainexecution.ArtifactReference, error) {
	if err := work.Validate(); err != nil {
		return domainexecution.ArtifactReference{}, fmt.Errorf("challenge publication execution work: %w", err)
	}
	if work.Artifact == nil || !challenge.ValidID(challengeID) {
		return domainexecution.ArtifactReference{}, errors.New("challenge publication requires a verified artifact and challenge identity")
	}
	switch work.Snapshot.Runtime {
	case challenge.RuntimeK8s:
		target, err := e.challengeImage(challengeID)
		if err != nil {
			return domainexecution.ArtifactReference{}, err
		}
		if err := e.registry.CopyImage(ctx, work.Artifact.OCIReference, target); err != nil {
			return domainexecution.ArtifactReference{}, fmt.Errorf("publish final challenge OCI image: %w", err)
		}
		immutable, err := e.resolveImmutable(ctx, target)
		if err != nil {
			return domainexecution.ArtifactReference{}, err
		}
		return domainexecution.ArtifactReference{Runtime: challenge.RuntimeK8s, OCIReference: immutable}, nil

	case challenge.RuntimeNode:
		if e.node == nil {
			return domainexecution.ArtifactReference{}, errors.New("node image publisher is unavailable")
		}
		published, err := e.node.PublishChallengeNodeImage(ctx, incus.PublishChallengeNodeImageRequest{
			CandidateRevisionID: work.CandidateID,
			ChallengeID:         challengeID,
			Staging: incus.PublishNodeImageResult{
				Alias: work.Artifact.IncusAlias, Fingerprint: work.Artifact.IncusFingerprint,
			},
		})
		if err != nil {
			return domainexecution.ArtifactReference{}, fmt.Errorf("publish final challenge Node image: %w", err)
		}
		return domainexecution.ArtifactReference{Runtime: challenge.RuntimeNode, IncusAlias: published.Alias, IncusFingerprint: published.Fingerprint}, nil
	default:
		return domainexecution.ArtifactReference{}, errors.New("candidate runtime is unsupported")
	}
}

func workFromGeneration(value generation.Execution) (domainexecution.Work, error) {
	if value.Context.Candidate == nil || value.Claim.Workflow.DeadlineAt == nil {
		return domainexecution.Work{}, errors.New("generation execution has no candidate or deadline")
	}
	view := value.Context.Candidate
	return domainexecution.Work{
		OwnerID: value.Claim.Workflow.ID, CandidateID: view.ID, ArchiveSHA256: view.ArchiveSHA256,
		Snapshot: view.Snapshot, Attempt: int64(value.Claim.StateAttempt + 1), DeadlineAt: value.Claim.Workflow.DeadlineAt.UTC(),
		Build: view.Build, Artifact: view.Artifact,
	}, nil
}

// Cleanup removes only deterministic resources for this workflow. It is safe
// to repeat after a process crash because each provider deletion is ownership
// checked and create-or-observe was used for every preceding phase.
func (e *Executor) Cleanup(ctx context.Context, execution generation.Execution) error {
	if !execution.Valid() || execution.Claim.Workflow.State != generation.StateCleaningUp || execution.Context.Candidate == nil {
		return errors.New("generation cleanup requires a CleaningUp workflow with a candidate")
	}
	view := execution.Context.Candidate
	switch view.Snapshot.Runtime {
	case challenge.RuntimeK8s:
		if view.Publication != nil && execution.Claim.Workflow.CleanupIntent != generation.CleanupCompleted {
			challengeImage, err := e.challengeImage(view.Publication.ChallengeID)
			if err != nil {
				return err
			}
			if err := e.registry.DeleteImage(ctx, challengeImage); err != nil {
				return fmt.Errorf("delete uncommitted challenge OCI image: %w", err)
			}
		}
		return e.discardCandidate(ctx, execution.Claim.Workflow.ID, *view)

	case challenge.RuntimeNode:
		if e.node == nil {
			return errors.New("node image publisher is unavailable")
		}
		fingerprint := cleanupNodeFingerprint(*view)
		if fingerprint != "" {
			if view.Publication != nil && execution.Claim.Workflow.CleanupIntent != generation.CleanupCompleted {
				if err := e.node.DeleteChallengeNodeImage(ctx, view.Publication.ChallengeID, fingerprint); err != nil {
					return fmt.Errorf("delete uncommitted challenge Node image: %w", err)
				}
			}
		}
		return e.discardCandidate(ctx, execution.Claim.Workflow.ID, *view)
	default:
		return errors.New("candidate runtime is unsupported")
	}
}

func (e *Executor) discardCandidate(ctx context.Context, workflowID string, view generation.WorkerView) error {
	switch view.Snapshot.Runtime {
	case challenge.RuntimeK8s:
		candidateImage, err := e.candidateImage(view.ID)
		if err != nil {
			return err
		}
		if view.Artifact != nil && view.Artifact.OCIReference != "" {
			candidateImage = view.Artifact.OCIReference
		}
		if view.Artifact == nil {
			return nil
		}
		if err := e.registry.DeleteImage(ctx, candidateImage); err != nil {
			return fmt.Errorf("delete candidate OCI image: %w", err)
		}
		return nil

	case challenge.RuntimeNode:
		if e.node == nil {
			return errors.New("node image publisher is unavailable")
		}
		if view.Artifact != nil && view.Artifact.IncusFingerprint != "" {
			if err := e.node.DeleteCandidateNodeImage(ctx, view.ID, view.Artifact.IncusFingerprint); err != nil {
				return fmt.Errorf("delete candidate Node image: %w", err)
			}
		}
		return e.cleanupNodeBuildAttempts(ctx, workflowID, view)
	default:
		return errors.New("candidate runtime is unsupported")
	}
}

func (e *Executor) cleanupNodeBuildAttempts(ctx context.Context, workflowID string, view generation.WorkerView) error {
	attempts := int64(0)
	if view.Build != nil && view.Build.Incus != nil {
		if view.Build.Incus.WorkflowID != workflowID {
			return errors.New("candidate Node build does not belong to generation workflow")
		}
		attempts = view.Build.Incus.Attempt
	}
	for attempt := int64(1); attempt <= attempts; attempt++ {
		if err := e.node.DeleteBuildNodeImageAttempt(ctx, workflowID, attempt); err != nil {
			return fmt.Errorf("delete Node build attempt %d: %w", attempt, err)
		}
	}
	return nil
}

func cleanupNodeFingerprint(view generation.WorkerView) string {
	if view.Artifact != nil && view.Artifact.IncusFingerprint != "" {
		return view.Artifact.IncusFingerprint
	}
	if view.Build != nil && view.Build.Incus != nil && view.Build.Incus.Fingerprint != "" {
		return view.Build.Incus.Fingerprint
	}
	return ""
}

func (e *Executor) resolveImmutable(ctx context.Context, tagged string) (string, error) {
	immutable, err := e.registry.ResolveImmutableReference(ctx, tagged)
	if err != nil {
		return "", fmt.Errorf("resolve published OCI image: %w", err)
	}
	if err := (generation.ArtifactReference{Runtime: challenge.RuntimeK8s, OCIReference: immutable}).Validate(challenge.RuntimeK8s); err != nil {
		return "", fmt.Errorf("Registry returned an invalid immutable OCI reference: %w", err)
	}
	return immutable, nil
}

func (e *Executor) candidateImage(candidateID string) (string, error) {
	return candidate.CandidateOCIImageReference(e.registryRepository, candidateID)
}

func (e *Executor) challengeImage(challengeID string) (string, error) {
	return candidate.ChallengeOCIImageReference(e.registryRepository, challengeID)
}

func nodeBuildResult(build *generation.BuildOutput) (incus.BuildNodeImageResult, error) {
	if build == nil || build.Incus == nil {
		return incus.BuildNodeImageResult{}, errors.New("candidate has no Node build identity")
	}
	return incus.BuildNodeImageResult{
		WorkflowID:   build.Incus.WorkflowID,
		Attempt:      build.Incus.Attempt,
		InstanceName: build.Incus.InstanceName,
		Alias:        build.Incus.Alias,
		Fingerprint:  build.Incus.Fingerprint,
	}, nil
}
