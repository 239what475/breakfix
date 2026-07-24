package controller

import (
	"context"
	"fmt"
	"time"

	breakfixv1 "github.com/breakfix/breakfix/apis/breakfix/v1"
	"github.com/breakfix/breakfix/internal/k8s"
	"github.com/breakfix/breakfix/pkg/vclustercli"
	ctrl "sigs.k8s.io/controller-runtime"
)

// Setup registers all reconcilers with the controller-runtime manager.
func Setup(mgr ctrl.Manager, k8sClient *k8s.Client, registryAddr, namespace, crdNamespace, challengesDir, dataDir string, cooldownMin int, registryInsecure bool, internalAPIKey, serverHost string, serverPort int, vclusterBinary, vclusterChartRepo, vclusterChartVersion string, completionRecorder CompletionRecorder) error {
	if err := breakfixv1.AddToScheme(mgr.GetScheme()); err != nil {
		return err
	}

	cooldown := time.Duration(cooldownMin) * time.Minute
	vclusterClient := &vclustercli.Client{BinaryPath: vclusterBinary}
	if _, err := vclusterClient.Validate(context.Background()); err != nil {
		return fmt.Errorf("validate vcluster cli: %w", err)
	}

	if err := (&GenerationReconciler{
		Client:       mgr.GetClient(),
		K8s:          k8sClient,
		CRDNamespace: crdNamespace,
	}).SetupWithManager(mgr); err != nil {
		return err
	}

	if err := (&VerifyTaskReconciler{
		Client:           mgr.GetClient(),
		K8s:              k8sClient,
		RegistryAddr:     registryAddr,
		RegistryInsecure: registryInsecure,
		CRDNamespace:     crdNamespace,
		InternalAPIKey:   internalAPIKey,
		ServerHost:       serverHost,
		ServerPort:       serverPort,
	}).SetupWithManager(mgr); err != nil {
		return err
	}

	if err := (&ContainerEnvironmentReconciler{
		Client:             mgr.GetClient(),
		K8s:                k8sClient,
		RegistryAddr:       registryAddr,
		ChallengesDir:      challengesDir,
		NS:                 namespace,
		CRDNamespace:       crdNamespace,
		Cooldown:           cooldown,
		CompletionRecorder: completionRecorder,
	}).SetupWithManager(mgr); err != nil {
		return err
	}

	if err := (&VClusterEnvironmentReconciler{
		Client:             mgr.GetClient(),
		K8s:                k8sClient,
		VCluster:           vclusterClient,
		ChartRepo:          vclusterChartRepo,
		ChartVersion:       vclusterChartVersion,
		RegistryAddr:       registryAddr,
		ChallengesDir:      challengesDir,
		NS:                 namespace,
		CRDNamespace:       crdNamespace,
		Cooldown:           cooldown,
		CompletionRecorder: completionRecorder,
	}).SetupWithManager(mgr); err != nil {
		return err
	}

	if err := startEnvironmentCleanupLoop(mgr, k8sClient, crdNamespace); err != nil {
		return err
	}

	return nil
}
