package controller

import (
	"context"
	"fmt"
	"time"

	"github.com/breakfix/breakfix/internal/k8s"
	breakfixv1 "github.com/breakfix/breakfix/internal/k8s/apis/breakfix/v1"
	"github.com/breakfix/breakfix/internal/vclustercli"
	ctrl "sigs.k8s.io/controller-runtime"
)

type Options struct {
	RegistryAddr         string
	RegistryInsecure     bool
	RegistryUsername     string
	RegistryPassword     string
	RegistryPullSecret   string
	RegistryWriteSecret  string
	Namespace            string
	CRDNamespace         string
	CooldownMinutes      int
	InternalAPIKey       string
	ServerHost           string
	ServerPort           int
	VClusterBinary       string
	VClusterChartRepo    string
	VClusterChartVersion string
}

// Setup registers all reconcilers with the controller-runtime manager. Its
// inputs are intentionally limited to Kubernetes and controller configuration;
// filesystem challenge data and the Server database belong to the Server.
func Setup(mgr ctrl.Manager, k8sClient *k8s.Client, opts Options) error {
	if err := breakfixv1.AddToScheme(mgr.GetScheme()); err != nil {
		return err
	}

	cooldown := time.Duration(opts.CooldownMinutes) * time.Minute
	vclusterClient := &vclustercli.Client{BinaryPath: opts.VClusterBinary}
	if _, err := vclusterClient.Validate(context.Background()); err != nil {
		return fmt.Errorf("validate vcluster cli: %w", err)
	}

	if err := (&VerifyTaskReconciler{
		Client:              mgr.GetClient(),
		K8s:                 k8sClient,
		RegistryAddr:        opts.RegistryAddr,
		RegistryInsecure:    opts.RegistryInsecure,
		RegistryUsername:    opts.RegistryUsername,
		RegistryPassword:    opts.RegistryPassword,
		RegistryPullSecret:  opts.RegistryPullSecret,
		RegistryWriteSecret: opts.RegistryWriteSecret,
		CRDNamespace:        opts.CRDNamespace,
		InternalAPIKey:      opts.InternalAPIKey,
		ServerHost:          opts.ServerHost,
		ServerPort:          opts.ServerPort,
	}).SetupWithManager(mgr); err != nil {
		return err
	}

	if err := (&ContainerEnvironmentReconciler{
		Client:             mgr.GetClient(),
		K8s:                k8sClient,
		RegistryAddr:       opts.RegistryAddr,
		RegistryPullSecret: opts.RegistryPullSecret,
		NS:                 opts.Namespace,
		CRDNamespace:       opts.CRDNamespace,
		Cooldown:           cooldown,
	}).SetupWithManager(mgr); err != nil {
		return err
	}

	if err := (&VClusterEnvironmentReconciler{
		Client:             mgr.GetClient(),
		K8s:                k8sClient,
		VCluster:           vclusterClient,
		ChartRepo:          opts.VClusterChartRepo,
		ChartVersion:       opts.VClusterChartVersion,
		RegistryAddr:       opts.RegistryAddr,
		RegistryPullSecret: opts.RegistryPullSecret,
		NS:                 opts.Namespace,
		CRDNamespace:       opts.CRDNamespace,
		Cooldown:           cooldown,
	}).SetupWithManager(mgr); err != nil {
		return err
	}

	return startEnvironmentCleanupLoop(mgr, k8sClient, opts.Namespace, opts.CRDNamespace)
}
