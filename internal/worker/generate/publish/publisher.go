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
	"time"

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
	DeleteBuildNodeImage(context.Context, incus.BuildNodeImageResult) error
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

func (e *Executor) PublishArtifact(ctx context.Context, value generation.Execution, buildArchive []byte) (generation.ArtifactReference, error) {
	if !value.Valid() || value.Claim.Workflow.State != generation.StateArtifactPublishing || value.Context.Candidate == nil {
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
	if !value.Valid() || value.Claim.Workflow.State != generation.StateChallengePublishing || value.Context.Candidate == nil {
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
	if value.Context.Candidate == nil {
		return domainexecution.Work{}, errors.New("generation execution has no candidate")
	}
	view := value.Context.Candidate
	return domainexecution.Work{
		OwnerID: value.Claim.Workflow.ID, CandidateID: view.ID, ArchiveSHA256: view.ArchiveSHA256,
		Snapshot: view.Snapshot, Attempt: value.Claim.StateVersion, DeadlineAt: domainexecution.NewActionDeadline(time.Now()),
		Build: view.Build, Artifact: view.Artifact,
	}, nil
}

// ReapCandidate removes candidate-scoped external resources after the Server
// has durably determined that no active workflow needs them. It does not
// mutate workflow state and is safe to repeat after a lost worker lease.
func (e *Executor) ReapCandidate(ctx context.Context, reap generation.ResourceReap) error {
	if !reap.Valid() {
		return errors.New("candidate resource reap is invalid")
	}
	switch reap.Kind {
	case generation.ResourceReapNodeBuildImage:
		return e.reapNodeBuildImage(ctx, reap.Candidate)
	case generation.ResourceReapCandidateArtifact:
		return e.reapCandidateArtifact(ctx, reap)
	default:
		return errors.New("generate worker does not own this resource reap")
	}
}

func (e *Executor) reapCandidateArtifact(ctx context.Context, reap generation.ResourceReap) error {
	view := reap.Candidate
	switch view.Snapshot.Runtime {
	case challenge.RuntimeK8s:
		if reap.DeleteFinalArtifact && view.Publication != nil {
			challengeImage, err := e.challengeImage(view.Publication.ChallengeID)
			if err != nil {
				return err
			}
			if err := e.registry.DeleteImage(ctx, challengeImage); err != nil {
				return fmt.Errorf("delete uncommitted challenge OCI image: %w", err)
			}
		}
		candidateImage, err := e.candidateImage(view.ID)
		if err != nil {
			return err
		}
		if view.Artifact != nil && view.Artifact.OCIReference != "" {
			candidateImage = view.Artifact.OCIReference
		}
		if err := e.registry.DeleteImage(ctx, candidateImage); err != nil {
			return fmt.Errorf("delete candidate OCI image: %w", err)
		}
		return nil

	case challenge.RuntimeNode:
		if e.node == nil {
			return errors.New("node image publisher is unavailable")
		}
		if reap.DeleteFinalArtifact && view.Publication != nil {
			fingerprint := view.Artifact
			if view.Publication.Artifact != nil {
				fingerprint = view.Publication.Artifact
			}
			if fingerprint != nil && fingerprint.IncusFingerprint != "" {
				if err := e.node.DeleteChallengeNodeImage(ctx, view.Publication.ChallengeID, fingerprint.IncusFingerprint); err != nil {
					return fmt.Errorf("delete uncommitted challenge Node image: %w", err)
				}
			}
		}
		fingerprint := ""
		if view.Artifact != nil {
			fingerprint = view.Artifact.IncusFingerprint
		} else if view.Build != nil && view.Build.Incus != nil {
			fingerprint = view.Build.Incus.Fingerprint
		}
		if fingerprint != "" {
			if err := e.node.DeleteCandidateNodeImage(ctx, view.ID, fingerprint); err != nil {
				return fmt.Errorf("delete candidate Node image: %w", err)
			}
		}
		return nil
	default:
		return errors.New("candidate runtime is unsupported")
	}
}

func (e *Executor) reapNodeBuildImage(ctx context.Context, view generation.WorkerView) error {
	if view.Snapshot.Runtime != challenge.RuntimeNode || view.Build == nil || view.Build.Incus == nil {
		return nil
	}
	if e.node == nil {
		return errors.New("node image publisher is unavailable")
	}
	build, err := nodeBuildResult(view.Build)
	if err != nil {
		return err
	}
	if build.CandidateRevisionID != view.ID {
		return errors.New("candidate Node build does not belong to candidate revision")
	}
	if err := e.node.DeleteBuildNodeImage(ctx, build); err != nil {
		return fmt.Errorf("delete Node build image: %w", err)
	}
	return nil
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
		WorkflowID: build.Incus.WorkflowID, CandidateRevisionID: build.Incus.CandidateRevisionID,
		Attempt: build.Incus.Attempt, InstanceName: build.Incus.InstanceName, Alias: build.Incus.Alias, Fingerprint: build.Incus.Fingerprint,
	}, nil
}
