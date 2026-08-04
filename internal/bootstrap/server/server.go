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
	appgeneration "github.com/breakfix/breakfix/internal/application/generation"
	"github.com/breakfix/breakfix/internal/bootstrap/config"
	"github.com/breakfix/breakfix/internal/bootstrap/runtimesnapshot"
	"github.com/breakfix/breakfix/internal/buildinfo"
	"github.com/breakfix/breakfix/internal/transport/httpapi"
	"github.com/breakfix/breakfix/internal/transport/httpapi/ui"
	"github.com/breakfix/breakfix/internal/worker/generate/build"
	"github.com/breakfix/breakfix/internal/worker/generate/publish"
	"github.com/breakfix/breakfix/internal/worker/generate/verify"
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
	var generatorSandbox *opensandbox.Client
	var generatorWorkspace *appgeneration.Manager
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
	}

	stopCatalogInstaller := func() {}
	if cfg.Catalog.Enabled() {
		deadline, err := cfg.Catalog.Deadline()
		if err != nil {
			incusClient.Close()
			cleanupDatabase()
			return nil, fmt.Errorf("parse catalog install deadline: %w", err)
		}
		publisherExecutor, err := publish.NewExecutor(registryClient, incusClient, cfg.Registry.Repository)
		if err != nil {
			incusClient.Close()
			cleanupDatabase()
			return nil, fmt.Errorf("create catalog publisher: %w", err)
		}
		verifierExecutor, err := verify.NewExecutor(k8sClient, incusClient, cfg.CRDNamespace)
		if err != nil {
			incusClient.Close()
			cleanupDatabase()
			return nil, fmt.Errorf("create catalog verifier: %w", err)
		}
		installer, err := appcatalog.NewInstaller(appcatalog.InstallerConfig{
			DataDir:          cfg.DataDir,
			ChallengesDir:    cfg.ChallengesDir(),
			ReleaseReference: cfg.Catalog.ReleaseReference,
			InstallDeadline:  deadline,
			LeaseTTL:         2 * time.Minute,
			PollInterval:     2 * time.Second,
			WorkerID:         catalogInstallerID(),
			Snapshot:         runtimesnapshot.From(cfg.Runtime, cfg.Incus),
			Puller:           registryClient,
			LayerReader:      oci.ArtifactLayerReader{ArtifactType: appcatalog.ReleaseArtifactType, LayerType: appcatalog.ReleaseSourceLayerType},
			Store:            database.Catalog,
			Roadmap:          database.Roadmap,
			Builder:          build.NewExecutor(incusClient, cfg.Incus),
			Publisher:        publisherExecutor,
			Verifier:         verifierExecutor,
		})
		if err != nil {
			incusClient.Close()
			cleanupDatabase()
			return nil, fmt.Errorf("create catalog installer: %w", err)
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
		RegistryClient:     registryClient,
		GeneratorSandbox:   generatorSandbox,
		GeneratorWorkspace: generatorWorkspace,
	})
	if err != nil {
		stopCatalogInstaller()
		incusClient.Close()
		cleanupDatabase()
		return nil, fmt.Errorf("setup HTTP API: %w", err)
	}

	slog.Info("Breakfix Server starting", "version", buildinfo.Version, "data_dir", cfg.DataDir)
	return &Runtime{
		server: &http.Server{
			Addr:              fmt.Sprintf(":%d", cfg.Port),
			Handler:           router,
			ReadHeaderTimeout: 10 * time.Second,
		},
		closeFn: func() {
			stopCatalogInstaller()
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
