// Package publish publishes and cleans immutable candidate artifacts for a
// GenerationWorkflow phase. It does not schedule work or mutate Server state.
package publish

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/breakfix/breakfix/internal/adapter/incus"
	"github.com/breakfix/breakfix/internal/adapter/oci"
	"github.com/breakfix/breakfix/internal/content/candidate"
	"github.com/breakfix/breakfix/internal/content/challenge"
	domainexecution "github.com/breakfix/breakfix/internal/domain/execution"
	runtime "github.com/breakfix/breakfix/internal/domain/runtime"
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

// PublishArtifactWork publishes an immutable staging artifact for a neutral
// execution owner. It deliberately does not perform any workflow transition.
func (e *Executor) PublishArtifactWork(ctx context.Context, work domainexecution.Work) (domainexecution.ArtifactReference, error) {
	if err := work.Validate(); err != nil {
		return domainexecution.ArtifactReference{}, fmt.Errorf("artifact publication execution work: %w", err)
	}
	if work.Build == nil {
		return domainexecution.ArtifactReference{}, errors.New("candidate has no build output")
	}
	switch work.Snapshot.Runtime {
	case challenge.RuntimeK8s:
		target, err := e.candidateImage(work.CandidateID)
		if err != nil {
			return domainexecution.ArtifactReference{}, err
		}
		immutable, err := e.promoteOCI(ctx, work.Build.OCIReference, target)
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
		immutable, err := e.promoteOCI(ctx, work.Artifact.OCIReference, target)
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

// promoteOCI is provider-side create-or-get for a stable runtime identity.
// A retry must observe the same immutable manifest rather than overwrite a
// tagged target that may have been created by an earlier lease holder.
func (e *Executor) promoteOCI(ctx context.Context, source, target string) (string, error) {
	sourceDigest, err := candidate.OCIDigest(source)
	if err != nil {
		return "", err
	}
	existing, resolveErr := e.registry.ResolveImmutableReference(ctx, target)
	if resolveErr == nil {
		if digest, digestErr := candidate.OCIDigest(existing); digestErr != nil || digest != sourceDigest {
			return "", domainexecution.NewArtifactError("ARTIFACT_REFERENCE_CONFLICT", "existing OCI artifact does not match this runtime action")
		}
		return existing, nil
	}
	if !errors.Is(resolveErr, oci.ErrReferenceNotFound) {
		return "", fmt.Errorf("resolve existing OCI artifact: %w", resolveErr)
	}
	if err := e.registry.CopyImage(ctx, source, target); err != nil {
		return "", fmt.Errorf("promote OCI artifact: %w", err)
	}
	immutable, err := e.resolveImmutable(ctx, target)
	if err != nil {
		return "", err
	}
	if digest, digestErr := candidate.OCIDigest(immutable); digestErr != nil || digest != sourceDigest {
		return "", domainexecution.NewArtifactError("ARTIFACT_REFERENCE_CONFLICT", "published OCI artifact does not match this runtime action")
	}
	return immutable, nil
}

// ReapCandidate removes candidate-scoped external resources after the Server
// has durably determined that no active workflow needs them. It does not
// mutate workflow state and is safe to repeat after a lost worker lease.
func (e *Executor) ReapResource(ctx context.Context, reap runtime.Reap) error {
	if err := reap.Valid(); err != nil {
		return errors.New("candidate resource reap is invalid")
	}
	switch reap.Kind {
	case runtime.ReapNodeBuildImage:
		return e.reapNodeBuildImage(ctx, reap)
	case runtime.ReapCandidateArtifact:
		return e.reapCandidateArtifact(ctx, reap)
	default:
		return errors.New("runtime worker does not own this resource reap")
	}
}

func (e *Executor) reapCandidateArtifact(ctx context.Context, reap runtime.Reap) error {
	switch reap.Snapshot.Runtime {
	case challenge.RuntimeK8s:
		if reap.Build != nil && reap.Build.OCIReference != "" {
			if err := e.registry.DeleteImage(ctx, reap.Build.OCIReference); err != nil {
				return fmt.Errorf("delete K8s build artifact: %w", err)
			}
		}
		if reap.DeleteFinalArtifact && reap.FinalArtifact != nil {
			if err := e.registry.DeleteImage(ctx, reap.FinalArtifact.OCIReference); err != nil {
				return fmt.Errorf("delete uncommitted challenge OCI image: %w", err)
			}
		}
		if reap.Artifact != nil && reap.Artifact.OCIReference != "" {
			if err := e.registry.DeleteImage(ctx, reap.Artifact.OCIReference); err != nil {
				return fmt.Errorf("delete candidate OCI image: %w", err)
			}
		}
		return nil

	case challenge.RuntimeNode:
		if e.node == nil {
			return errors.New("node image publisher is unavailable")
		}
		if reap.DeleteFinalArtifact && reap.FinalArtifact != nil && reap.FinalArtifact.IncusFingerprint != "" {
			if err := e.node.DeleteChallengeNodeImage(ctx, reap.ChallengeID, reap.FinalArtifact.IncusFingerprint); err != nil {
				return fmt.Errorf("delete uncommitted challenge Node image: %w", err)
			}
		}
		fingerprint := ""
		if reap.Artifact != nil {
			fingerprint = reap.Artifact.IncusFingerprint
		}
		if fingerprint != "" {
			if err := e.node.DeleteCandidateNodeImage(ctx, reap.ResourceID, fingerprint); err != nil {
				return fmt.Errorf("delete candidate Node image: %w", err)
			}
		}
		return nil
	default:
		return errors.New("candidate runtime is unsupported")
	}
}

func (e *Executor) reapNodeBuildImage(ctx context.Context, reap runtime.Reap) error {
	if reap.Snapshot.Runtime != challenge.RuntimeNode || reap.Build == nil || reap.Build.Incus == nil {
		return nil
	}
	if e.node == nil {
		return errors.New("node image publisher is unavailable")
	}
	build, err := nodeBuildResult(reap.Build)
	if err != nil {
		return err
	}
	if build.CandidateRevisionID != reap.ResourceID {
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
	if err := (domainexecution.ArtifactReference{Runtime: challenge.RuntimeK8s, OCIReference: immutable}).Validate(challenge.RuntimeK8s); err != nil {
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

func nodeBuildResult(build *domainexecution.BuildOutput) (incus.BuildNodeImageResult, error) {
	if build == nil || build.Incus == nil {
		return incus.BuildNodeImageResult{}, errors.New("candidate has no Node build identity")
	}
	return incus.BuildNodeImageResult{
		WorkflowID: build.Incus.WorkflowID, CandidateRevisionID: build.Incus.CandidateRevisionID,
		Attempt: build.Incus.Attempt, InstanceName: build.Incus.InstanceName, Alias: build.Incus.Alias, Fingerprint: build.Incus.Fingerprint,
	}, nil
}
