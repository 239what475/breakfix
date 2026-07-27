package controller

import (
	"context"
	"log/slog"
	"time"

	"github.com/breakfix/breakfix/internal/k8s"
	ctrl "sigs.k8s.io/controller-runtime"
)

const cleanupSweepInterval = 2 * time.Minute

type cleanupLoop struct {
	k8sClient            *k8s.Client
	environmentNamespace string
	crdNamespace         string
}

func startEnvironmentCleanupLoop(mgr ctrl.Manager, k8sClient *k8s.Client, environmentNamespace, crdNamespace string) error {
	return mgr.Add(&cleanupLoop{
		k8sClient:            k8sClient,
		environmentNamespace: environmentNamespace,
		crdNamespace:         crdNamespace,
	})
}

func (l *cleanupLoop) Start(runCtx context.Context) error {
	ticker := time.NewTicker(cleanupSweepInterval)
	defer ticker.Stop()

	sweepCtx, cancel := context.WithTimeout(runCtx, 30*time.Second)
	if err := cleanupStaleEnvironments(sweepCtx, l.k8sClient, l.environmentNamespace, l.crdNamespace); err != nil {
		cancel()
		return err
	}
	cancel()

	for {
		select {
		case <-runCtx.Done():
			return nil
		case <-ticker.C:
			sweepCtx, cancel := context.WithTimeout(runCtx, 30*time.Second)
			if err := cleanupStaleEnvironments(sweepCtx, l.k8sClient, l.environmentNamespace, l.crdNamespace); err != nil {
				slog.Error("cleanup sweep failed", "err", err)
			}
			cancel()
		}
	}
}
