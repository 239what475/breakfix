package controller

import (
	"context"
	"fmt"
	"strings"

	breakfixv1 "github.com/breakfix/breakfix/api/v1"
	"github.com/breakfix/breakfix/internal/adapter/kubernetes"
	"github.com/breakfix/breakfix/internal/adapter/vcluster"
	"github.com/breakfix/breakfix/internal/controller/nodeenvironment"
	"github.com/breakfix/breakfix/internal/controller/vk8senvironment"
	environmentdomain "github.com/breakfix/breakfix/internal/domain/environment"
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
	NodeProvider environmentdomain.NodeProvider
	VK8sProvider environmentdomain.VK8sProvider
}

// Setup registers only the two final Environment reconcilers. Workflow state
// belongs to Server/PostgreSQL and is never reconciled by this process.
func Setup(manager ctrl.Manager, k8sClient *kubernetes.Client, options Options, dependencies Dependencies) error {
	if err := breakfixv1.AddToScheme(manager.GetScheme()); err != nil {
		return err
	}
	if strings.TrimSpace(options.Namespace) == "" || strings.TrimSpace(options.CRDNamespace) == "" {
		return fmt.Errorf("runtime namespace and CRD namespace are required")
	}

	vk8sProvider := dependencies.VK8sProvider
	if vk8sProvider == nil {
		vclusterClient := &vcluster.Client{BinaryPath: options.VClusterBinary}
		if _, err := vclusterClient.Validate(context.Background()); err != nil {
			return fmt.Errorf("validate vcluster CLI: %w", err)
		}
		if strings.TrimSpace(options.VClusterChartRepo) == "" || strings.TrimSpace(options.VClusterChartVersion) == "" {
			return fmt.Errorf("vcluster chart repository and version are required")
		}
		vk8sProvider = kubernetes.NewVK8sEnvironmentProvider(k8sClient, vclusterClient, kubernetes.VK8sEnvironmentProviderConfig{
			NamespacePrefix: options.Namespace, ControlNamespace: options.CRDNamespace,
			RegistryPullSecret: options.RegistryPullSecret, VerificationServiceAccount: "breakfix-runtime-worker",
			ChartRepo: options.VClusterChartRepo, ChartVersion: options.VClusterChartVersion,
		})
	}

	nodeProvider := dependencies.NodeProvider
	if nodeProvider == nil {
		nodeProvider = nodeenvironment.UnavailableProvider(nil)
	}
	if err := (&nodeenvironment.NodeEnvironmentReconciler{Client: manager.GetClient(), Provider: nodeProvider}).SetupWithManager(manager); err != nil {
		return err
	}
	return (&vk8senvironment.VK8sEnvironmentReconciler{Client: manager.GetClient(), Provider: vk8sProvider}).SetupWithManager(manager)
}
