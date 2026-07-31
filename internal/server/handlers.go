package server

import (
	"fmt"
	"time"

	"github.com/breakfix/breakfix/internal/assistant"
	"github.com/breakfix/breakfix/internal/authoring"
	"github.com/breakfix/breakfix/internal/config"
	"github.com/breakfix/breakfix/internal/db"
	"github.com/breakfix/breakfix/internal/incusprovider"
	"github.com/breakfix/breakfix/internal/k8s"
	"github.com/breakfix/breakfix/internal/opensandbox"
	"github.com/breakfix/breakfix/internal/registry"
	"github.com/breakfix/breakfix/internal/taxonomy"
	"github.com/breakfix/breakfix/internal/workspace"
)

// Handler owns the Server's shared dependencies. HTTP handlers are separated
// by domain so routing stays stable while each endpoint's responsibility is local.
type Handler struct {
	db                 *db.DB
	k8s                *k8s.Client
	authoring          *authoring.RuntimeService
	assistant          *assistant.Service
	registryAddr       string
	registryClient     registry.Client
	namespace          string
	crdNamespace       string
	challengesDir      string
	taxonomy           *taxonomy.Store
	taxonomyWorkflow   *taxonomy.Service
	dataDir            string
	cooldownMin        int
	llm                config.AgentConfig
	jwtSecret          []byte
	internalWorkers    config.InternalWorkerKeys
	port               int
	uiOrigin           string
	startupErr         error
	terminals          *terminalConnectionTracker
	serverInstance     string
	generatorSandbox   *opensandbox.Client
	generatorWorkspace *workspace.Manager
	runtimeConfig      config.RuntimeConfig
	incusConfig        incusprovider.Config
	nodeTerminal       NodeTerminalProvider
	nodeProviderReady  NodeProviderReadiness
	worklistMetrics    *worklistOperationMetrics
}

type Dependencies struct {
	NodeTerminal NodeTerminalProvider
}

func NewHandler(database *db.DB, client *k8s.Client, cfg config.Config) *Handler {
	return NewHandlerWithDependencies(database, client, cfg, Dependencies{})
}

func NewHandlerWithDependencies(database *db.DB, client *k8s.Client, cfg config.Config, dependencies Dependencies) *Handler {
	taxonomyStore := taxonomy.NewStore(cfg.DataDir)
	registryClient, registryErr := registry.NewClient(registry.ClientOptions{
		Credentials:     registry.Credentials{Username: cfg.Registry.Username, Password: cfg.Registry.Password},
		TrustBundleFile: cfg.Registry.TrustBundleFile,
	})
	handler := &Handler{
		db:              database,
		k8s:             client,
		authoring:       authoring.NewRuntimeService(database, cfg.Agent.Model),
		assistant:       assistant.NewService(database, cfg.Agent.Model),
		registryAddr:    cfg.Registry.Address,
		registryClient:  registryClient,
		namespace:       cfg.Namespace,
		crdNamespace:    cfg.CRDNamespace,
		challengesDir:   cfg.ChallengesDir(),
		taxonomy:        taxonomyStore,
		dataDir:         cfg.DataDir,
		cooldownMin:     cfg.CooldownMinutes,
		llm:             cfg.Agent,
		jwtSecret:       []byte(cfg.JWTSecret),
		internalWorkers: cfg.InternalWorkers,
		port:            cfg.Port,
		uiOrigin:        cfg.UIOrigin,
		terminals:       newTerminalConnectionTracker(time.Second),
		serverInstance:  newServerInstanceID(),
		runtimeConfig:   cfg.Runtime,
		incusConfig:     cfg.Incus,
		nodeTerminal:    dependencies.NodeTerminal,
		worklistMetrics: newWorklistOperationMetrics(),
	}
	if ready, ok := dependencies.NodeTerminal.(NodeProviderReadiness); ok {
		handler.nodeProviderReady = ready
	}
	if registryErr != nil {
		handler.startupErr = fmt.Errorf("initialize registry client: %w", registryErr)
		return handler
	}
	if database != nil {
		handler.taxonomyWorkflow = taxonomy.NewService(database, taxonomyStore, cfg.ChallengesDir(), cfg.Agent)
	}
	if database != nil && client != nil && cfg.OpenSandbox.APIKey != "" {
		sandbox, err := opensandbox.New(cfg.OpenSandbox)
		if err != nil {
			handler.startupErr = fmt.Errorf("initialize opensandbox client: %w", err)
			return handler
		}
		provisionTimeout, err := cfg.OpenSandbox.ProvisionTimeout()
		if err != nil {
			handler.startupErr = fmt.Errorf("parse opensandbox workspace provision timeout: %w", err)
			return handler
		}
		manager, err := workspace.NewManager(database, client, sandbox, workspace.Config{
			Namespace:        cfg.OpenSandbox.Namespace,
			Storage:          cfg.OpenSandbox.WorkspaceStorage,
			ProvisionTimeout: provisionTimeout,
		})
		if err != nil {
			handler.startupErr = fmt.Errorf("initialize generator workspace manager: %w", err)
			return handler
		}
		handler.generatorSandbox = sandbox
		handler.generatorWorkspace = manager
	}
	return handler
}
