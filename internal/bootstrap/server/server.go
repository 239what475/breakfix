package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/breakfix/breakfix/internal/adapter/incus"
	"github.com/breakfix/breakfix/internal/adapter/kubernetes"
	"github.com/breakfix/breakfix/internal/adapter/llm"
	"github.com/breakfix/breakfix/internal/adapter/oci"
	"github.com/breakfix/breakfix/internal/adapter/opensandbox"
	"github.com/breakfix/breakfix/internal/adapter/postgres"
	appassistant "github.com/breakfix/breakfix/internal/application/assistant"
	appauthoring "github.com/breakfix/breakfix/internal/application/authoring"
	appcatalog "github.com/breakfix/breakfix/internal/application/catalog"
	appexecution "github.com/breakfix/breakfix/internal/application/execution"
	appgeneration "github.com/breakfix/breakfix/internal/application/generation"
	appinteractive "github.com/breakfix/breakfix/internal/application/interactive"
	applearning "github.com/breakfix/breakfix/internal/application/learning"
	apppublication "github.com/breakfix/breakfix/internal/application/publication"
	approadmap "github.com/breakfix/breakfix/internal/application/roadmap"
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
	server            *http.Server
	services          *serviceLifecycle
	closeDependencies func()
	closeOnce         sync.Once
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
	var generatorService *appgeneration.GeneratorService
	var generationAgents *appgeneration.AgentRunner
	var workspaceReaper *appgeneration.WorkspaceReaper
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
		generatorService, err = appgeneration.NewGeneratorService(
			database.Generation,
			database.Authoring,
			generatorWorkspace,
			generatorSandbox,
			appgeneration.GeneratorServiceConfig{
				DataDir: cfg.DataDir,
				FreezeExecution: func(entry challenge.Entry) (generationdomain.ExecutionSnapshot, error) {
					return appexecution.Freeze(entry, runtimesnapshot.From(cfg.Runtime, cfg.Incus))
				},
			},
		)
		if err != nil {
			incusClient.Close()
			cleanupDatabase()
			return nil, fmt.Errorf("create generator service: %w", err)
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
		generationAgents, err = appgeneration.NewAgentRunner(database.Generation, llm.NewJudge(cfg.Agent), classifierExecutor, appgeneration.AgentRunnerConfig{
			ServerID: catalogInstallerID(), Model: cfg.Agent.Model,
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
	if generatorWorkspace != nil {
		workspaceReaper, err = appgeneration.NewWorkspaceReaper(generatorWorkspace)
		if err != nil {
			incusClient.Close()
			cleanupDatabase()
			return nil, fmt.Errorf("create generator workspace reaper: %w", err)
		}
		if err := workspaceReaper.Recover(ctx); err != nil {
			incusClient.Close()
			cleanupDatabase()
			return nil, fmt.Errorf("recover generator workspaces: %w", err)
		}
	}

	var catalogInstaller *appcatalog.Installer
	if cfg.Catalog.Enabled() {
		catalogInstaller, err = appcatalog.NewInstaller(appcatalog.InstallerConfig{
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
		if err := catalogInstaller.ValidateBootstrap(ctx); err != nil {
			incusClient.Close()
			cleanupDatabase()
			return nil, fmt.Errorf("validate catalog bootstrap: %w", err)
		}
	}

	availability, err := appcatalog.NewAvailability(cfg.Catalog.ReleaseReference, database.Catalog)
	if err != nil {
		incusClient.Close()
		cleanupDatabase()
		return nil, fmt.Errorf("create catalog availability gate: %w", err)
	}
	catalogService := appcatalog.NewService(cfg.ChallengesDir(), database.Roadmap, availability, database.Challenge)
	if err := catalogService.CheckIntegrity(ctx); err != nil {
		incusClient.Close()
		cleanupDatabase()
		return nil, fmt.Errorf("validate challenge catalog: %w", err)
	}

	if generatorService == nil {
		incusClient.Close()
		cleanupDatabase()
		return nil, errors.New("generator service is required for authoring")
	}
	authoringService := appauthoring.NewRuntimeService(database.Authoring, cfg.Agent.Model, llm.NewAuthoringExecutor(cfg.Agent, generatorService))
	assistantService := appassistant.NewService(database.Agent, cfg.Agent.Model, llm.NewAssistantExecutor(cfg.Agent))
	roadmapMaintenance, err := approadmap.NewMaintenanceService(approadmap.MaintenanceConfig{
		Repository: database.Roadmap, Executor: llm.NewRoadmapExecutor(cfg.Agent),
		ChallengeReader: approadmap.FilesystemChallengeReader{Root: cfg.ChallengesDir()},
		Model:           cfg.Agent.Model, ServerID: catalogInstallerID(),
	})
	if err != nil {
		incusClient.Close()
		cleanupDatabase()
		return nil, fmt.Errorf("create roadmap maintenance service: %w", err)
	}
	cleanupService, err := applearning.NewCleanupService(learningStore{repository: database.Environment})
	if err != nil {
		incusClient.Close()
		cleanupDatabase()
		return nil, fmt.Errorf("create learning cleanup service: %w", err)
	}
	projectionService, err := applearning.NewProjectionService(
		environmentProjectionSource{client: k8sClient, namespace: cfg.CRDNamespace},
		learningStore{repository: database.Environment},
	)
	if err != nil {
		incusClient.Close()
		cleanupDatabase()
		return nil, fmt.Errorf("create learning projection service: %w", err)
	}
	publicationFinalizer, err := appgeneration.NewPublicationFinalizer(appgeneration.PublicationFinalizerConfig{
		Store: database.Generation, ChallengesDir: cfg.ChallengesDir(),
		Validator: challengeArtifactValidator{registryRepository: cfg.Registry.Repository, incusNamePrefix: cfg.Incus.NamePrefix},
	})
	if err != nil {
		incusClient.Close()
		cleanupDatabase()
		return nil, fmt.Errorf("create generation publication finalizer: %w", err)
	}
	services := newServiceLifecycle()
	// The Handler is the transport adapter for the two Environment-dependent
	// lifecycle ports. It receives the service context but starts nothing.
	frontendFS, err := ui.Filesystem()
	if err != nil {
		incusClient.Close()
		cleanupDatabase()
		return nil, fmt.Errorf("load embedded web assets: %w", err)
	}
	serviceContext := services.ctx
	handler, err := httpapi.NewHandlerWithDependencies(database, k8sClient, cfg, httpapi.Dependencies{
		NodeTerminal:        incusClient,
		Assistant:           assistantService,
		Authoring:           authoringService,
		Catalog:             catalogService,
		AgentRuntimeContext: serviceContext,
		Generator:           generatorService,
	})
	if err != nil {
		services.stop()
		incusClient.Close()
		cleanupDatabase()
		return nil, fmt.Errorf("create HTTP handler: %w", err)
	}
	interactiveRecovery, err := appinteractive.NewRecovery(appinteractive.RecoveryConfig{
		Repository: database.Agent, Authoring: authoringService, Assistant: assistantService, AssistantRequest: handler,
	})
	if err != nil {
		services.stop()
		incusClient.Close()
		cleanupDatabase()
		return nil, fmt.Errorf("create interactive recovery: %w", err)
	}
	leaseMaintainer, err := appassistant.NewLeaseMaintainer(database.Agent, handler)
	if err != nil {
		services.stop()
		incusClient.Close()
		cleanupDatabase()
		return nil, fmt.Errorf("create assistant lease maintainer: %w", err)
	}
	if err := cleanupService.Recover(ctx); err != nil {
		services.stop()
		incusClient.Close()
		cleanupDatabase()
		return nil, fmt.Errorf("recover learning cleanup: %w", err)
	}
	if err := projectionService.Recover(ctx); err != nil {
		services.stop()
		incusClient.Close()
		cleanupDatabase()
		return nil, fmt.Errorf("recover learning projection: %w", err)
	}
	if err := publicationFinalizer.Recover(ctx); err != nil {
		services.stop()
		incusClient.Close()
		cleanupDatabase()
		return nil, fmt.Errorf("recover generation publication finalizer: %w", err)
	}
	if err := roadmapMaintenance.Recover(ctx); err != nil {
		services.stop()
		incusClient.Close()
		cleanupDatabase()
		return nil, fmt.Errorf("recover roadmap maintenance: %w", err)
	}
	if err := interactiveRecovery.Recover(ctx); err != nil {
		services.stop()
		incusClient.Close()
		cleanupDatabase()
		return nil, fmt.Errorf("recover interactive agent runs: %w", err)
	}
	if err := leaseMaintainer.Recover(ctx); err != nil {
		services.stop()
		incusClient.Close()
		cleanupDatabase()
		return nil, fmt.Errorf("recover assistant environment leases: %w", err)
	}
	router, err := httpapi.SetupRouter(handler, cfg, frontendFS)
	if err != nil {
		services.stop()
		incusClient.Close()
		cleanupDatabase()
		return nil, fmt.Errorf("setup HTTP API: %w", err)
	}

	// Every process-scoped loop is started explicitly and is waited by Close.
	if catalogInstaller != nil {
		services.start("catalog installer", catalogInstaller.Run)
	}
	services.start("challenge materialization reconciler", materializations.Run)
	if generationAgents != nil {
		services.start("generation agent runtime", generationAgents.Run)
	}
	if workspaceReaper != nil {
		services.start("generator workspace reaper", workspaceReaper.Run)
	}
	services.start("learning cleanup", cleanupService.Run)
	services.start("learning environment projection", projectionService.Run)
	services.start("assistant environment lease maintenance", leaseMaintainer.Run)
	services.start("generation publication finalizer", publicationFinalizer.Run)
	services.start("roadmap maintenance", roadmapMaintenance.Run)
	services.start("interactive agent recovery", interactiveRecovery.Run)

	slog.Info("Breakfix Server starting", "version", buildinfo.Version, "data_dir", cfg.DataDir)
	return &Runtime{
		server: &http.Server{
			Addr:              fmt.Sprintf(":%d", cfg.Port),
			Handler:           router,
			ReadHeaderTimeout: 10 * time.Second,
		},
		services: services,
		closeDependencies: func() {
			incusClient.Close()
			cleanupDatabase()
		},
	}, nil
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
		r.Close()
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := r.server.Shutdown(shutdownCtx); err != nil {
			r.Close()
			return fmt.Errorf("shutdown server: %w", err)
		}
		err := <-result
		r.Close()
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func (r *Runtime) Close() {
	if r == nil {
		return
	}
	r.closeOnce.Do(func() {
		if r.services != nil {
			r.services.stop()
		}
		if r.closeDependencies != nil {
			r.closeDependencies()
		}
	})
}

func Run(ctx context.Context, configPath string) error {
	runtime, err := New(ctx, configPath)
	if err != nil {
		return err
	}
	defer runtime.Close()
	return runtime.Run(ctx)
}
