package controller

import (
	"time"

	"github.com/breakfix/breakfix/internal/k8s"
	breakfixv1 "github.com/breakfix/breakfix/apis/breakfix/v1"
	ctrl "sigs.k8s.io/controller-runtime"
)

// Setup registers all reconcilers with the controller-runtime manager.
func Setup(mgr ctrl.Manager, k8sClient *k8s.Client, registryAddr, namespace, crdNamespace string, cooldownMin int) error {
	if err := breakfixv1.AddToScheme(mgr.GetScheme()); err != nil {
		return err
	}

	cooldown := time.Duration(cooldownMin) * time.Minute

	if err := (&GenerationReconciler{
		Client:        mgr.GetClient(),
		K8s:           k8sClient,
		CRDNamespace:  crdNamespace,
	}).SetupWithManager(mgr); err != nil {
		return err
	}

	if err := (&InstanceReconciler{
		Client:        mgr.GetClient(),
		K8s:           k8sClient,
		RegistryAddr:  registryAddr,
		NS:            namespace,
		CRDNamespace:  crdNamespace,
		Cooldown:      cooldown,
	}).SetupWithManager(mgr); err != nil {
		return err
	}

	return nil
}
