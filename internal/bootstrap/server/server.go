package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/adapter/incus"
	"github.com/breakfix/breakfix/internal/adapter/kubernetes"
	"github.com/breakfix/breakfix/internal/adapter/llm"
	"github.com/breakfix/breakfix/internal/adapter/oci"
	"github.com/breakfix/breakfix/internal/adapter/opensandbox"
	"github.com/breakfix/breakfix/internal/adapter/postgres"
	appcatalog "github.com/breakfix/breakfix/internal/application/catalog"
	appexecution "github.com/breakfix/breakfix/internal/application/execution"
	appgeneration "github.com/breakfix/breakfix/internal/application/generation"
	apppublication "github.com/breakfix/breakfix/internal/application/publication"
	"github.com/breakfix/breakfix/internal/bootstrap/config"
	"github.com/breakfix/breakfix/internal/bootstrap/runtimesnapshot"
	"github.com/breakfix/breakfix/internal/buildinfo"
	"github.com/breakfix/breakfix/internal/content/challenge"
	generationdomain "github.com/breakfix/breakfix/internal/domain/generation"
	"github.com/breakfix/breakfix/internal/transport/httpapi"
	"github.com/breakfix/breakfix/internal/transport/httpapi/ui"
)

// Runtime owns process-scoped Server resources. Application and transport code
// receive only the dependencies they need during assembly below.
type Runtime struct {
	server  *http.Server
	closeFn func()
}

func New(ctx context.Context, configPath string) (*Runtime, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, fmt.Errorf("load configuration: %w", err)
	}
	if err := cfg.ValidateServer(); err != nil {
		return nil, fmt.Errorf("validate server configuration: %w", err)
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	if err := os.MkdirAll(cfg.ChallengesDir(), 0o755); err != nil {
		return nil, fmt.Errorf("create challenges directory: %w", err)
	}

	database, err := postgres.New(cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	cleanupDatabase := func() { _ = database.Close() }

	k8sClient, err := kubernetes.New(cfg.Kubeconfig)
	if err != nil {
		cleanupDatabase()
		return nil, fmt.Errorf("create Kubernetes client: %w", err)
	}
	incusClient, err := incus.NewReconnectableClient(cfg.Incus, incus.RoleServer)
	if err != nil {
		cleanupDatabase()
		return nil, fmt.Errorf("create Incus client: %w", err)
	}
	registryAuthority, err := oci.AuthorityForReference(cfg.Registry.Repository)
	if err != nil {
		incusClient.Close()
		cleanupDatabase()
		return nil, fmt.Errorf("derive Registry authority: %w", err)
	}
	registryClient, err := oci.NewClient(oci.ClientOptions{
		Authority:       registryAuthority,
		Credentials:     oci.Credentials{Username: cfg.Registry.Username, Password: cfg.Registry.Password},
		TrustBundleFile: cfg.Registry.TrustBundleFile,
	})
	if err != nil {
		incusClient.Close()
		cleanupDatabase()
		return nil, fmt.Errorf("create OCI registry client: %w", err)
	}
	materializations, err := apppublication.NewMaterializationReconciler(database.Publication, apppublication.MaterializationReconcilerConfig{
		ChallengesDir: cfg.ChallengesDir(),
	})
	if err != nil {
		incusClient.Close()
		cleanupDatabase()
		return nil, fmt.Errorf("create challenge materialization reconciler: %w", err)
	}
	if err := materializations.Recover(ctx); err != nil {
		incusClient.Close()
		cleanupDatabase()
		return nil, fmt.Errorf("recover challenge materializations: %w", err)
	}
	var generatorSandbox *opensandbox.Client
	var generatorWorkspace *appgeneration.Manager
	var generationAgents *appgeneration.AgentRunner
	if cfg.OpenSandbox.APIKey != "" {
		generatorSandbox, err = opensandbox.New(cfg.OpenSandbox)
		if err != nil {
			incusClient.Close()
			cleanupDatabase()
			return nil, fmt.Errorf("create OpenSandbox client: %w", err)
		}
		provisionTimeout, err := cfg.OpenSandbox.ProvisionTimeout()
		if err != nil {
			incusClient.Close()
			cleanupDatabase()
			return nil, fmt.Errorf("parse OpenSandbox workspace provision timeout: %w", err)
		}
		generatorWorkspace, err = appgeneration.NewManager(database.Generation, k8sClient, generatorSandbox, appgeneration.Config{
			Namespace:        cfg.OpenSandbox.Namespace,
			Storage:          cfg.OpenSandbox.WorkspaceStorage,
			ProvisionTimeout: provisionTimeout,
		})
		if err != nil {
			incusClient.Close()
			cleanupDatabase()
			return nil, fmt.Errorf("create generator workspace manager: %w", err)
		}
		workspaceRuntime, err := appgeneration.NewWorkspaceRuntime(database.Generation, generatorWorkspace, generatorSandbox)
		if err != nil {
			incusClient.Close()
			cleanupDatabase()
			return nil, fmt.Errorf("create generator workspace runtime: %w", err)
		}
		generatorExecutor, err := llm.NewGeneratorExecutor(cfg.Agent, workspaceRuntime)
		if err != nil {
			incusClient.Close()
			cleanupDatabase()
			return nil, fmt.Errorf("create generator agent executor: %w", err)
		}
		classificationRuntime, err := appgeneration.NewClassificationRuntime(database.Generation, database.Agent, database.Roadmap)
		if err != nil {
			incusClient.Close()
			cleanupDatabase()
			return nil, fmt.Errorf("create classification runtime: %w", err)
		}
		classifierExecutor, err := llm.NewClassifier(cfg.Agent, classificationRuntime)
		if err != nil {
			incusClient.Close()
			cleanupDatabase()
			return nil, fmt.Errorf("create classification agent executor: %w", err)
		}
		snapshot := runtimesnapshot.From(cfg.Runtime, cfg.Incus)
		generationAgents, err = appgeneration.NewAgentRunner(database.Generation, generatorExecutor, classifierExecutor, generatorWorkspace, appgeneration.AgentRunnerConfig{
			ServerID: catalogInstallerID(), Model: cfg.Agent.Model, DataDir: cfg.DataDir,
			FreezeExecution: func(entry challenge.Entry) (generationdomain.ExecutionSnapshot, error) {
				return appexecution.Freeze(entry, snapshot)
			},
		})
		if err != nil {
			incusClient.Close()
			cleanupDatabase()
			return nil, fmt.Errorf("create generation agent runner: %w", err)
		}
		if err := generationAgents.Recover(ctx); err != nil {
			incusClient.Close()
			cleanupDatabase()
			return nil, fmt.Errorf("recover generation agent runtime: %w", err)
		}
	}

	stopCatalogInstaller := func() {}
	if cfg.Catalog.Enabled() {
		installer, err := appcatalog.NewInstaller(appcatalog.InstallerConfig{
			DataDir:          cfg.DataDir,
			ChallengesDir:    cfg.ChallengesDir(),
			ReleaseReference: cfg.Catalog.ReleaseReference,
			PollInterval:     2 * time.Second,
			Snapshot:         runtimesnapshot.From(cfg.Runtime, cfg.Incus),
			Puller:           registryClient,
			LayerReader:      oci.ArtifactLayerReader{ArtifactType: appcatalog.ReleaseArtifactType, LayerType: appcatalog.ReleaseSourceLayerType},
			Store:            database.Catalog,
		})
		if err != nil {
			incusClient.Close()
			cleanupDatabase()
			return nil, fmt.Errorf("create catalog installer: %w", err)
		}
		if err := installer.ValidateBootstrap(ctx); err != nil {
			incusClient.Close()
			cleanupDatabase()
			return nil, fmt.Errorf("validate catalog bootstrap: %w", err)
		}
		installerCtx, cancelInstaller := context.WithCancel(ctx)
		installerDone := make(chan struct{})
		go func() {
			defer close(installerDone)
			if err := installer.Run(installerCtx); err != nil && installerCtx.Err() == nil {
				slog.Error("catalog installer stopped", "err", err)
			}
		}()
		stopCatalogInstaller = func() {
			cancelInstaller()
			<-installerDone
		}
	}
	frontendFS, err := ui.Filesystem()
	if err != nil {
		stopCatalogInstaller()
		incusClient.Close()
		cleanupDatabase()
		return nil, fmt.Errorf("load embedded web assets: %w", err)
	}
	router, err := httpapi.SetupRouter(ctx, database, k8sClient, cfg, frontendFS, httpapi.Dependencies{
		NodeTerminal:       incusClient,
		AssistantExecutor:  llm.NewAssistantExecutor(cfg.Agent),
		AuthoringExecutor:  llm.NewAuthoringExecutor(cfg.Agent),
		RoadmapExecutor:    llm.NewRoadmapExecutor(cfg.Agent),
		GeneratorWorkspace: generatorWorkspace,
	})
	if err != nil {
		stopCatalogInstaller()
		incusClient.Close()
		cleanupDatabase()
		return nil, fmt.Errorf("setup HTTP API: %w", err)
	}
	stopGeneratorWorkspaceCleanup := startGeneratorWorkspaceCleanup(ctx, generatorWorkspace)
	materializationCtx, cancelMaterializations := context.WithCancel(ctx)
	materializationDone := make(chan struct{})
	go func() {
		defer close(materializationDone)
		if err := materializations.Run(materializationCtx); err != nil && materializationCtx.Err() == nil {
			slog.Error("challenge materialization reconciler stopped", "err", err)
		}
	}()
	stopMaterializations := func() {
		cancelMaterializations()
		<-materializationDone
	}
	stopGenerationAgents := func() {}
	if generationAgents != nil {
		agentCtx, cancelAgents := context.WithCancel(ctx)
		agentDone := make(chan struct{})
		go func() {
			defer close(agentDone)
			if err := generationAgents.Run(agentCtx); err != nil && agentCtx.Err() == nil {
				slog.Error("generation agent runtime stopped", "err", err)
			}
		}()
		stopGenerationAgents = func() {
			cancelAgents()
			<-agentDone
		}
	}

	slog.Info("Breakfix Server starting", "version", buildinfo.Version, "data_dir", cfg.DataDir)
	return &Runtime{
		server: &http.Server{
			Addr:              fmt.Sprintf(":%d", cfg.Port),
			Handler:           router,
			ReadHeaderTimeout: 10 * time.Second,
		},
		closeFn: func() {
			stopGenerationAgents()
			stopGeneratorWorkspaceCleanup()
			stopCatalogInstaller()
			stopMaterializations()
			incusClient.Close()
			cleanupDatabase()
		},
	}, nil
}

func startGeneratorWorkspaceCleanup(parent context.Context, manager *appgeneration.Manager) func() {
	if manager == nil {
		return func() {}
	}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			if err := manager.CleanupDue(ctx); err != nil && ctx.Err() == nil {
				slog.Warn("clean generator workspaces", "err", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

func catalogInstallerID() string {
	if value := strings.TrimSpace(os.Getenv("POD_NAME")); value != "" {
		return "catalog-" + value
	}
	if host, err := os.Hostname(); err == nil && strings.TrimSpace(host) != "" {
		return "catalog-" + strings.TrimSpace(host)
	}
	return "catalog-server"
}

func (r *Runtime) Run(ctx context.Context) error {
	if r == nil || r.server == nil {
		return errors.New("server runtime is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	result := make(chan error, 1)
	go func() { result <- r.server.ListenAndServe() }()

	select {
	case err := <-result:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := r.server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown server: %w", err)
		}
		err := <-result
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func (r *Runtime) Close() {
	if r != nil && r.closeFn != nil {
		r.closeFn()
	}
}

func Run(ctx context.Context, configPath string) error {
	runtime, err := New(ctx, configPath)
	if err != nil {
		return err
	}
	defer runtime.Close()
	return runtime.Run(ctx)
}
