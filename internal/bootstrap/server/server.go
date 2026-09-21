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
	appdoc "github.com/breakfix/breakfix/internal/application/documentpractice"
	appgeneration "github.com/breakfix/breakfix/internal/application/generation"
	appinteractive "github.com/breakfix/breakfix/internal/application/interactive"
	applearning "github.com/breakfix/breakfix/internal/application/learning"
	apppublication "github.com/breakfix/breakfix/internal/application/publication"
	"github.com/breakfix/breakfix/internal/bootstrap/config"
	"github.com/breakfix/breakfix/internal/buildinfo"
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
	if err := os.MkdirAll(cfg.ScenariosDir(), 0o755); err != nil {
		return nil, fmt.Errorf("create scenarios directory: %w", err)
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
	// A Node-less deployment omits the Incus endpoint; the nil client keeps
	// every Node operation failing with an explicit "not configured" error.
	var incusClient *incus.ReconnectableClient
	if cfg.Incus.Enabled() {
		incusClient, err = incus.NewReconnectableClient(cfg.Incus, incus.RoleServer)
		if err != nil {
			cleanupDatabase()
			return nil, fmt.Errorf("create Incus client: %w", err)
		}
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
		ScenariosDir: cfg.ScenariosDir(),
	})
	if err != nil {
		incusClient.Close()
		cleanupDatabase()
		return nil, fmt.Errorf("create scenario materialization reconciler: %w", err)
	}
	if err := materializations.Recover(ctx); err != nil {
		incusClient.Close()
		cleanupDatabase()
		return nil, fmt.Errorf("recover scenario materializations: %w", err)
	}
	var generatorSandbox *opensandbox.Client
	var generatorWorkspace *appgeneration.Manager
	var generatorService *appgeneration.GeneratorService
	var generationAgents *appgeneration.AgentRunner
	var generationRunnable *appgeneration.RunnableCoordinator
	var workspaceReaper *appgeneration.WorkspaceReaper
	var workspaceSnapshotter *appgeneration.WorkspaceSnapshotter
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
		workspaceIdleTTL, err := cfg.WorkspaceIdleTTL()
		if err != nil {
			incusClient.Close()
			cleanupDatabase()
			return nil, fmt.Errorf("parse generator workspace idle ttl: %w", err)
		}
		workspaceSnapshotter, err = appgeneration.NewWorkspaceSnapshotter(generatorWorkspace, generatorSandbox, appgeneration.WorkspaceSnapshotterConfig{
			DataDir: cfg.DataDir,
			IdleTTL: workspaceIdleTTL,
		})
		if err != nil {
			incusClient.Close()
			cleanupDatabase()
			return nil, fmt.Errorf("create generator workspace snapshotter: %w", err)
		}
		generatorService, err = appgeneration.NewGeneratorService(
			database.Generation,
			database.Authoring,
			generatorWorkspace,
			generatorSandbox,
			appgeneration.GeneratorServiceConfig{
				DataDir:           cfg.DataDir,
				SnapshotRequested: workspaceSnapshotter.Request,
			},
		)
		if err != nil {
			incusClient.Close()
			cleanupDatabase()
			return nil, fmt.Errorf("create generator service: %w", err)
		}
		generationAgents, err = appgeneration.NewAgentRunner(database.Generation, llm.NewJudge(cfg.Agent), appgeneration.AgentRunnerConfig{
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

	operationsConfig, err := operationsRuntimeConfig(cfg)
	if err != nil {
		incusClient.Close()
		cleanupDatabase()
		return nil, fmt.Errorf("configure Operations runnable publisher: %w", err)
	}
	generationRunnable, err = appgeneration.NewRunnableCoordinator(
		database.Generation,
		database.Runnable,
		operationsConfig,
		2*time.Second,
	)
	if err != nil {
		incusClient.Close()
		cleanupDatabase()
		return nil, fmt.Errorf("create generation runnable coordinator: %w", err)
	}
	documentationPipeline, documentationService, documentationLibrary, err := newDocumentationPipeline(cfg, database)
	if err != nil {
		incusClient.Close()
		cleanupDatabase()
		return nil, fmt.Errorf("configure documentation practice pipeline: %w", err)
	}
	if documentationPipeline != nil {
		// Workflows created before page identity columns existed carry their
		// corpus coordinates only inside the ledger; enrich them once at boot.
		if _, err := database.DocumentPractice.BackfillWorkflowPageIdentity(ctx); err != nil {
			incusClient.Close()
			cleanupDatabase()
			return nil, fmt.Errorf("backfill documentation workflow page identity: %w", err)
		}
	}

	documentationBatches := newDocumentationBatches(documentationService, documentationLibrary)
	var documentationScheduler *appdoc.BatchScheduler
	if documentationPipeline != nil {
		documentationScheduler, err = newDocumentationBatchScheduler(documentationService, documentationPipeline)
		if err != nil {
			incusClient.Close()
			cleanupDatabase()
			return nil, fmt.Errorf("create documentation batch scheduler: %w", err)
		}
	}

	var catalogInstaller *appcatalog.Installer
	if cfg.Catalog.Enabled() {
		catalogInstaller, err = appcatalog.NewInstaller(appcatalog.InstallerConfig{
			DataDir:          cfg.DataDir,
			ScenariosDir:     cfg.ScenariosDir(),
			ReleaseReference: cfg.Catalog.ReleaseReference,
			PollInterval:     2 * time.Second,
			Puller:           registryClient,
			LayerReader:      oci.ArtifactLayerReader{ArtifactType: appcatalog.ReleaseArtifactType, LayerType: appcatalog.ReleaseSourceLayerType},
			Store:            database.Catalog,
			Runnable:         database.Runnable,
			Operations:       operationsConfig,
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
	catalogService := appcatalog.NewService(cfg.ScenariosDir(), availability, database.Scenario, database.Runnable)
	if err := catalogService.CheckIntegrity(ctx); err != nil {
		incusClient.Close()
		cleanupDatabase()
		return nil, fmt.Errorf("validate scenario catalog: %w", err)
	}
	if generatorService == nil {
		incusClient.Close()
		cleanupDatabase()
		return nil, errors.New("generator service is required for authoring")
	}
	authoringDeadline, err := cfg.Agent.AuthoringDeadline()
	if err != nil {
		incusClient.Close()
		cleanupDatabase()
		return nil, fmt.Errorf("parse authoring run deadline: %w", err)
	}
	authoringService := appauthoring.NewRuntimeService(database.Authoring, cfg.Agent.Model, authoringDeadline, llm.NewAuthoringExecutor(cfg.Agent, generatorService))
	assistantService := appassistant.NewService(database.Agent, cfg.Agent.Model, llm.NewAssistantExecutor(cfg.Agent))
	cleanupService, err := applearning.NewCleanupService(learningStore{repository: database.Environment})
	if err != nil {
		incusClient.Close()
		cleanupDatabase()
		return nil, fmt.Errorf("create learning cleanup service: %w", err)
	}
	projectionService, err := applearning.NewProjectionService(
		environmentProjectionSource{client: k8sClient, namespace: cfg.CRDNamespace, evaluator: operationsLearningCheckpointEvaluator{revisions: database.Runnable, node: incusClient, pods: k8sClient}},
		learningStore{repository: database.Environment},
	)
	if err != nil {
		incusClient.Close()
		cleanupDatabase()
		return nil, fmt.Errorf("create learning projection service: %w", err)
	}
	publicationFinalizer, err := appgeneration.NewPublicationFinalizer(appgeneration.PublicationFinalizerConfig{
		Store: database.Generation, Runnable: database.Runnable, ScenariosDir: cfg.ScenariosDir(),
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
	documentationApplication, err := newFixedDocumentationApplication(documentationPipeline, documentationLibrary)
	if err != nil {
		services.stop()
		incusClient.Close()
		cleanupDatabase()
		return nil, fmt.Errorf("create documentation application: %w", err)
	}
	handler, err := httpapi.NewHandlerWithDependencies(database, k8sClient, cfg, httpapi.Dependencies{
		NodeTerminal:         incusClient,
		Assistant:            assistantService,
		Authoring:            authoringService,
		Catalog:              catalogService,
		AgentRuntimeContext:  serviceContext,
		Generator:            generatorService,
		Documentation:        documentationApplication,
		DocumentationBatches: documentationBatches,
		DocumentationLibrary: documentationLibrary,
		SystemReport:         newSystemReportProvider(cfg, services.registry, documentationLibrary).Report,
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
	if err := generationRunnable.Recover(ctx); err != nil {
		services.stop()
		incusClient.Close()
		cleanupDatabase()
		return nil, fmt.Errorf("recover generation runnable coordinator: %w", err)
	}
	if documentationPipeline != nil {
		if err := documentationPipeline.Recover(ctx); err != nil {
			services.stop()
			incusClient.Close()
			cleanupDatabase()
			return nil, fmt.Errorf("recover documentation practice workflow: %w", err)
		}
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
	// Each service also reports its passes into the in-memory registry so the
	// admin system endpoint can show real tick times and errors.
	if catalogInstaller != nil {
		catalogInstaller.OnTick = services.tickObserver("catalog installer")
		services.start("catalog installer", catalogInstaller.Run)
	}
	materializations.OnTick = services.tickObserver("scenario materialization reconciler")
	services.start("scenario materialization reconciler", materializations.Run)
	if generationAgents != nil {
		generationAgents.OnTick = services.tickObserver("generation agent runtime")
		services.start("generation agent runtime", generationAgents.Run)
	}
	generationRunnable.OnTick = services.tickObserver("generation runnable coordinator")
	services.start("generation runnable coordinator", generationRunnable.Run)
	if workspaceReaper != nil {
		workspaceReaper.OnTick = services.tickObserver("generator workspace reaper")
		services.start("generator workspace reaper", workspaceReaper.Run)
	}
	if workspaceSnapshotter != nil {
		workspaceSnapshotter.OnTick = services.tickObserver("generator workspace snapshotter")
		services.start("generator workspace snapshotter", workspaceSnapshotter.Run)
	}
	cleanupService.OnTick = services.tickObserver("learning cleanup")
	services.start("learning cleanup", cleanupService.Run)
	projectionService.OnTick = services.tickObserver("learning environment projection")
	services.start("learning environment projection", projectionService.Run)
	leaseMaintainer.OnTick = services.tickObserver("assistant environment lease maintenance")
	services.start("assistant environment lease maintenance", leaseMaintainer.Run)
	publicationFinalizer.OnTick = services.tickObserver("generation publication finalizer")
	services.start("generation publication finalizer", publicationFinalizer.Run)
	if documentationPipeline != nil {
		documentationPipeline.OnTick = services.tickObserver("documentation practice action reconciler")
		services.start("documentation practice action reconciler", documentationPipeline.Run)
		documentationScheduler.OnTick = services.tickObserver("documentation batch scheduler")
		services.start("documentation batch scheduler", documentationScheduler.Run)
	}
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
