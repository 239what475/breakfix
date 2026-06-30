package main

import (
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	breakfixv1 "github.com/breakfix/breakfix/apis/breakfix/v1"
	"github.com/breakfix/breakfix/internal/config"
	"github.com/breakfix/breakfix/internal/controller"
	"github.com/breakfix/breakfix/internal/k8s"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
)

func main() {
	configPath := flag.String("config", "breakfix.yaml", "Config file path")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("failed to load config", "err", err)
		os.Exit(1)
	}

	k8sClient, err := k8s.New(cfg.Kubeconfig)
	if err != nil {
		slog.Error("failed to create K8s client", "err", err)
		os.Exit(1)
	}

	scheme := runtime.NewScheme()
	if err := breakfixv1.AddToScheme(scheme); err != nil {
		slog.Error("failed to add scheme", "err", err)
		os.Exit(1)
	}

	mgr, err := ctrl.NewManager(k8sClient.RESTConfig(), ctrl.Options{
		Scheme:                 scheme,
		HealthProbeBindAddress: ":" + strconv.Itoa(cfg.HealthPort),
		LeaderElection:         false,
	})
	if err != nil {
		slog.Error("failed to create manager", "err", err)
		os.Exit(1)
	}

	if err := controller.Setup(mgr, k8sClient, cfg.RegistryAddr, cfg.Namespace, cfg.CRDNamespace, cfg.CooldownMinutes); err != nil {
		slog.Error("failed to setup controllers", "err", err)
		os.Exit(1)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		slog.Info("controller starting", "namespace", cfg.Namespace, "crd_ns", cfg.CRDNamespace)
		if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
			slog.Error("controller error", "err", err)
			os.Exit(1)
		}
	}()

	<-sig
	slog.Info("controller shutting down")
}
