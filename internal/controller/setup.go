package controller

import (
	"context"
	"fmt"
	"strings"

	"github.com/breakfix/breakfix/internal/k8s"
	breakfixv1 "github.com/breakfix/breakfix/internal/k8s/apis/breakfix/v1"
	"github.com/breakfix/breakfix/internal/vclustercli"
	ctrl "sigs.k8s.io/controller-runtime"
)

type Options struct {
	Namespace            string
	CRDNamespace         string
	RegistryPullSecret   string
	VClusterBinary       string
	VClusterChartRepo    string
	VClusterChartVersion string
}

type Dependencies struct {
	NodeProvider NodeEnvironmentProvider
	VK8sProvider VK8sEnvironmentProvider
}

// Setup registers only the two final Environment reconcilers. Workflow state
// belongs to Server/PostgreSQL and is never reconciled by this process.
func Setup(manager ctrl.Manager, k8sClient *k8s.Client, options Options, dependencies Dependencies) error {
	if err := breakfixv1.AddToScheme(manager.GetScheme()); err != nil {
		return err
	}
	if strings.TrimSpace(options.Namespace) == "" || strings.TrimSpace(options.CRDNamespace) == "" {
		return fmt.Errorf("runtime namespace and CRD namespace are required")
	}

	vk8sProvider := dependencies.VK8sProvider
	if vk8sProvider == nil {
		vclusterClient := &vclustercli.Client{BinaryPath: options.VClusterBinary}
		if _, err := vclusterClient.Validate(context.Background()); err != nil {
			return fmt.Errorf("validate vcluster CLI: %w", err)
		}
		if strings.TrimSpace(options.VClusterChartRepo) == "" || strings.TrimSpace(options.VClusterChartVersion) == "" {
			return fmt.Errorf("vcluster chart repository and version are required")
		}
		vk8sProvider = &kubernetesVK8sProvider{
			k8s: k8sClient, vcluster: vclusterClient,
			namespacePrefix: options.Namespace, controlNamespace: options.CRDNamespace,
			registryPullSecret: options.RegistryPullSecret, verifierServiceAccount: "breakfix-verifier",
			chartRepo: options.VClusterChartRepo, chartVersion: options.VClusterChartVersion,
		}
	}

	nodeProvider := dependencies.NodeProvider
	if nodeProvider == nil {
		nodeProvider = UnavailableNodeProvider(nil)
	}
	if err := (&NodeEnvironmentReconciler{Client: manager.GetClient(), Provider: nodeProvider}).SetupWithManager(manager); err != nil {
		return err
	}
	return (&VK8sEnvironmentReconciler{Client: manager.GetClient(), Provider: vk8sProvider}).SetupWithManager(manager)
}
