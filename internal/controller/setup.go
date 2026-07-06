package controller

import (
	"time"

	breakfixv1 "github.com/breakfix/breakfix/apis/breakfix/v1"
	"github.com/breakfix/breakfix/internal/k8s"
	ctrl "sigs.k8s.io/controller-runtime"
)

// Setup registers all reconcilers with the controller-runtime manager.
func Setup(mgr ctrl.Manager, k8sClient *k8s.Client, registryAddr, namespace, crdNamespace, challengesDir, dataDir string, cooldownMin int, registryInsecure bool, internalAPIKey, serverHost string, serverPort int) error {
	if err := breakfixv1.AddToScheme(mgr.GetScheme()); err != nil {
		return err
	}

	cooldown := time.Duration(cooldownMin) * time.Minute

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
		ChallengesDir:    challengesDir,
		DataDir:          dataDir,
		InternalAPIKey:   internalAPIKey,
		ServerHost:       serverHost,
		ServerPort:       serverPort,
	}).SetupWithManager(mgr); err != nil {
		return err
	}

	if err := (&ContainerEnvironmentReconciler{
		Client:       mgr.GetClient(),
		K8s:          k8sClient,
		RegistryAddr: registryAddr,
		NS:           namespace,
		CRDNamespace: crdNamespace,
		Cooldown:     cooldown,
	}).SetupWithManager(mgr); err != nil {
		return err
	}

	if err := (&VClusterEnvironmentReconciler{
		Client:       mgr.GetClient(),
		K8s:          k8sClient,
		RegistryAddr: registryAddr,
		NS:           namespace,
		CRDNamespace: crdNamespace,
		Cooldown:     cooldown,
	}).SetupWithManager(mgr); err != nil {
		return err
	}

	if err := startEnvironmentCleanupLoop(mgr, k8sClient, crdNamespace); err != nil {
		return err
	}

	return nil
}
