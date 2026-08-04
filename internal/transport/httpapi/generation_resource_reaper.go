package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/breakfix/breakfix/internal/content/candidate"
	"github.com/breakfix/breakfix/internal/content/challenge"
	"github.com/breakfix/breakfix/internal/domain/generation"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
)

const generationResourceReapInterval = 10 * time.Second

// StartGenerationResourceReaper owns infrastructure that lives with Server:
// verification CRDs and local K8s build archives. Its leases are independent
// from GenerationWorkflow state, so terminal transitions are never delayed by
// a provider outage.
func (h *Handler) StartGenerationResourceReaper(ctx context.Context) {
	if h == nil || h.db == nil {
		return
	}
	go func() {
		for {
			for {
				processed, err := h.reapOneGenerationResource(ctx)
				if err != nil {
					slog.Warn("reap generation resource", "err", err)
					break
				}
				if !processed {
					break
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(generationResourceReapInterval):
			}
		}
	}()
}

func (h *Handler) reapOneGenerationResource(ctx context.Context) (bool, error) {
	if h == nil || h.db == nil {
		return false, nil
	}
	for _, kind := range []generation.ResourceReapKind{
		generation.ResourceReapVerificationEnvironment,
		generation.ResourceReapBuildArchive,
	} {
		claim, err := h.db.Generation.ClaimGenerationResourceReap(ctx, kind, h.serverInstance, generationMaxLease, time.Now().UTC())
		if err != nil {
			return false, err
		}
		if claim == nil {
			continue
		}
		failure := ""
		if err := h.reapGenerationResource(ctx, claim.ResourceReap); err != nil {
			failure = err.Error()
		}
		if err := h.db.Generation.CompleteGenerationResourceReap(ctx, *claim, failure, time.Now().UTC()); err != nil {
			return true, err
		}
		return true, nil
	}
	return false, nil
}

func (h *Handler) reapGenerationResource(ctx context.Context, reap generation.ResourceReap) error {
	if !reap.Valid() || reap.Kind.Owner() != "server" {
		return errors.New("server generation resource reap is invalid")
	}
	switch reap.Kind {
	case generation.ResourceReapVerificationEnvironment:
		return h.deleteGenerationVerificationEnvironment(ctx, reap.Candidate)
	case generation.ResourceReapBuildArchive:
		if err := candidate.RemoveBuildArchives(h.dataDir, reap.CandidateRevisionID); err != nil {
			return fmt.Errorf("remove generation build archives: %w", err)
		}
		return nil
	default:
		return errors.New("server does not own this generation resource reap")
	}
}

func (h *Handler) deleteGenerationVerificationEnvironment(ctx context.Context, view generation.WorkerView) error {
	if view.VerifyEnvironment == nil {
		return nil
	}
	if err := view.VerifyEnvironment.Validate(view.Snapshot.Runtime); err != nil {
		return err
	}
	if h.k8s == nil {
		return errors.New("Kubernetes client is unavailable for verification environment cleanup")
	}
	uid := types.UID(view.VerifyEnvironment.UID)
	var err error
	switch view.Snapshot.Runtime {
	case challenge.RuntimeNode:
		err = h.k8s.DeleteNodeEnvironmentWithUID(ctx, h.crdNamespace, view.VerifyEnvironment.Name, uid)
	case challenge.RuntimeK8s:
		err = h.k8s.DeleteVK8sEnvironmentWithUID(ctx, h.crdNamespace, view.VerifyEnvironment.Name, uid)
	default:
		return errors.New("verification environment has an unsupported runtime")
	}
	if apierrors.IsNotFound(err) || apierrors.IsConflict(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete verification environment: %w", err)
	}
	return nil
}
