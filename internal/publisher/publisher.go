// Package publisher implements the fixed artifact publication, cleanup, and
// final challenge publication stages. It never executes candidate files.
package publisher

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/breakfix/breakfix/internal/candidate"
	"github.com/breakfix/breakfix/internal/candidateworker"
	"github.com/breakfix/breakfix/internal/challenge"
	"github.com/breakfix/breakfix/internal/incusprovider"
	"github.com/breakfix/breakfix/internal/registry"
	"github.com/breakfix/breakfix/internal/worklist"
)

type Registry interface {
	PushOCIArchive(context.Context, string, string) error
	ResolveImmutableReference(context.Context, string) (string, error)
	CopyImage(context.Context, string, string) error
	DeleteImage(context.Context, string) error
}

type NodeImagePublisher interface {
	PublishNodeImage(context.Context, incusprovider.PublishNodeImageRequest) (incusprovider.PublishNodeImageResult, error)
	PublishChallengeNodeImage(context.Context, incusprovider.PublishChallengeNodeImageRequest) (incusprovider.PublishNodeImageResult, error)
	DeleteCandidateNodeImage(context.Context, string, string) error
	DeleteBuildNodeImage(context.Context, incusprovider.BuildNodeImageResult, string) error
	DeleteBuildNodeImageAttempt(context.Context, string, int64, string) error
	DeleteChallengeNodeImage(context.Context, string, string) error
}

type Executor struct {
	client       *candidateworker.Client
	registry     Registry
	node         NodeImagePublisher
	registryRoot string
}

func NewExecutor(client *candidateworker.Client, registryClient Registry, node NodeImagePublisher, registryRoot string) (*Executor, error) {
	if client == nil || registryClient == nil || strings.TrimSpace(registryRoot) == "" {
		return nil, errors.New("publisher requires Server and Registry clients plus a Registry root")
	}
	return &Executor{client: client, registry: registryClient, node: node, registryRoot: strings.TrimRight(strings.TrimSpace(registryRoot), "/")}, nil
}

func (e *Executor) Execute(ctx context.Context, claim candidateworker.Claim) error {
	switch claim.Work.Item.Kind {
	case worklist.KindArtifactPublish:
		return e.publishArtifact(ctx, claim)
	case worklist.KindArtifactCleanup:
		return e.cleanupArtifact(ctx, claim)
	case worklist.KindChallengePublish:
		return e.publishChallenge(ctx, claim)
	default:
		return fmt.Errorf("publisher cannot execute work kind %q", claim.Work.Item.Kind)
	}
}

func (e *Executor) publishArtifact(ctx context.Context, claim candidateworker.Claim) error {
	if claim.Candidate.Build == nil {
		return errors.New("candidate has no build output")
	}
	switch claim.Candidate.Snapshot.Runtime {
	case challenge.RuntimeK8s:
		archive, digest, err := e.client.DownloadBuildArchive(ctx, claim)
		if err != nil {
			return err
		}
		if digest != claim.Candidate.Build.OCIArchiveSHA256 || candidate.Digest(archive) != digest {
			return errors.New("candidate OCI archive does not match the recorded build output")
		}
		root, err := os.MkdirTemp("", "breakfix-publisher-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(root) //nolint:errcheck
		path := filepath.Join(root, "candidate.oci.tar")
		if err := os.WriteFile(path, archive, 0o400); err != nil {
			return err
		}
		if err := registry.ValidateOCIArchive(path); err != nil {
			return fmt.Errorf("validate Server build archive: %w", err)
		}
		target, err := e.candidateImage(claim.Candidate.ID)
		if err != nil {
			return err
		}
		if err := e.registry.PushOCIArchive(ctx, target, path); err != nil {
			return fmt.Errorf("publish candidate OCI image: %w", err)
		}
		immutable, err := e.resolveImmutable(ctx, target)
		if err != nil {
			return err
		}
		return e.client.CompleteArtifactPublish(ctx, claim, candidate.ArtifactReference{
			Runtime: challenge.RuntimeK8s, OCIReference: immutable,
		})

	case challenge.RuntimeNode:
		if e.node == nil {
			return errors.New("node image publisher is unavailable")
		}
		build, err := nodeBuildResult(claim.Candidate.Build)
		if err != nil {
			return err
		}
		published, err := e.node.PublishNodeImage(ctx, incusprovider.PublishNodeImageRequest{
			CandidateRevisionID: claim.Candidate.ID,
			Revision:            claim.Candidate.ArchiveSHA256,
			Build:               build,
		})
		if err != nil {
			return fmt.Errorf("publish candidate Node image: %w", err)
		}
		return e.client.CompleteArtifactPublish(ctx, claim, candidate.ArtifactReference{
			Runtime: challenge.RuntimeNode, IncusAlias: published.Alias, IncusFingerprint: published.Fingerprint,
		})
	default:
		return errors.New("candidate runtime is unsupported")
	}
}

func (e *Executor) publishChallenge(ctx context.Context, claim candidateworker.Claim) error {
	publication := claim.Candidate.Publication
	artifact := claim.Candidate.Artifact
	if publication == nil || artifact == nil {
		return errors.New("candidate challenge publication has no intent or verified artifact")
	}
	switch claim.Candidate.Snapshot.Runtime {
	case challenge.RuntimeK8s:
		target, err := e.challengeImage(publication.ChallengeID)
		if err != nil {
			return err
		}
		if err := e.registry.CopyImage(ctx, artifact.OCIReference, target); err != nil {
			return fmt.Errorf("publish final challenge OCI image: %w", err)
		}
		immutable, err := e.resolveImmutable(ctx, target)
		if err != nil {
			return err
		}
		return e.client.CompleteChallengePublish(ctx, claim, candidate.ArtifactReference{
			Runtime: challenge.RuntimeK8s, OCIReference: immutable,
		})

	case challenge.RuntimeNode:
		if e.node == nil {
			return errors.New("node image publisher is unavailable")
		}
		published, err := e.node.PublishChallengeNodeImage(ctx, incusprovider.PublishChallengeNodeImageRequest{
			CandidateRevisionID: claim.Candidate.ID,
			ChallengeID:         publication.ChallengeID,
			Staging: incusprovider.PublishNodeImageResult{
				Alias: artifact.IncusAlias, Fingerprint: artifact.IncusFingerprint,
			},
		})
		if err != nil {
			return fmt.Errorf("publish final challenge Node image: %w", err)
		}
		return e.client.CompleteChallengePublish(ctx, claim, candidate.ArtifactReference{
			Runtime: challenge.RuntimeNode, IncusAlias: published.Alias, IncusFingerprint: published.Fingerprint,
		})
	default:
		return errors.New("candidate runtime is unsupported")
	}
}

func (e *Executor) cleanupArtifact(ctx context.Context, claim candidateworker.Claim) error {
	if claim.Cleanup == nil || claim.Cleanup.Validate() != nil {
		return errors.New("candidate cleanup has no deterministic build identity")
	}
	switch claim.Candidate.Snapshot.Runtime {
	case challenge.RuntimeK8s:
		if claim.Candidate.Publication != nil && claim.Candidate.State != candidate.StatePublished {
			challengeImage, err := e.challengeImage(claim.Candidate.Publication.ChallengeID)
			if err != nil {
				return err
			}
			if err := e.registry.DeleteImage(ctx, challengeImage); err != nil {
				return fmt.Errorf("delete uncommitted challenge OCI image: %w", err)
			}
		}
		candidateImage, err := e.candidateImage(claim.Candidate.ID)
		if err != nil {
			return err
		}
		if claim.Candidate.Artifact != nil {
			candidateImage = claim.Candidate.Artifact.OCIReference
		}
		if err := e.registry.DeleteImage(ctx, candidateImage); err != nil {
			return fmt.Errorf("delete candidate OCI image: %w", err)
		}
	case challenge.RuntimeNode:
		if e.node == nil {
			return errors.New("node image publisher is unavailable")
		}
		for attempt := int64(1); attempt <= claim.Cleanup.BuildAttempts; attempt++ {
			if err := e.node.DeleteBuildNodeImageAttempt(ctx, claim.Cleanup.BuildWorkItemID, attempt, claim.Candidate.ArchiveSHA256); err != nil {
				return fmt.Errorf("delete Node build attempt %d: %w", attempt, err)
			}
		}
		fingerprint := cleanupNodeFingerprint(claim.Candidate)
		if fingerprint != "" {
			if claim.Candidate.Publication != nil && claim.Candidate.State != candidate.StatePublished {
				if err := e.node.DeleteChallengeNodeImage(ctx, claim.Candidate.Publication.ChallengeID, fingerprint); err != nil {
					return fmt.Errorf("delete uncommitted challenge Node image: %w", err)
				}
			}
			if err := e.node.DeleteCandidateNodeImage(ctx, claim.Candidate.ID, fingerprint); err != nil {
				return fmt.Errorf("delete candidate Node image: %w", err)
			}
		}
	default:
		return errors.New("candidate runtime is unsupported")
	}
	return e.client.CompleteCleanup(ctx, claim)
}

func cleanupNodeFingerprint(candidateView candidate.WorkerView) string {
	if candidateView.Artifact != nil && candidateView.Artifact.IncusFingerprint != "" {
		return candidateView.Artifact.IncusFingerprint
	}
	if candidateView.Build != nil && candidateView.Build.Incus != nil && candidateView.Build.Incus.Fingerprint != "" {
		return candidateView.Build.Incus.Fingerprint
	}
	return ""
}

func (e *Executor) resolveImmutable(ctx context.Context, tagged string) (string, error) {
	immutable, err := e.registry.ResolveImmutableReference(ctx, tagged)
	if err != nil {
		return "", fmt.Errorf("resolve published OCI image: %w", err)
	}
	if err := (candidate.ArtifactReference{
		Runtime: challenge.RuntimeK8s, OCIReference: immutable,
	}).Validate(challenge.RuntimeK8s); err != nil {
		return "", fmt.Errorf("Registry returned an invalid immutable OCI reference: %w", err)
	}
	return immutable, nil
}

func (e *Executor) candidateImage(candidateID string) (string, error) {
	return candidate.CandidateOCIImageReference(e.registryRoot, candidateID)
}

func (e *Executor) challengeImage(challengeID string) (string, error) {
	return candidate.ChallengeOCIImageReference(e.registryRoot, challengeID)
}

func nodeBuildResult(build *candidate.BuildOutput) (incusprovider.BuildNodeImageResult, error) {
	if build == nil || build.Incus == nil {
		return incusprovider.BuildNodeImageResult{}, errors.New("candidate has no Node build identity")
	}
	return incusprovider.BuildNodeImageResult{
		WorkItemID: build.Incus.WorkItemID, Attempt: build.Incus.Attempt,
		InstanceName: build.Incus.InstanceName, Alias: build.Incus.Alias, Fingerprint: build.Incus.Fingerprint,
	}, nil
}
