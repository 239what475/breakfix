package server

import (
	"fmt"
	"sync"
	"time"

	"github.com/breakfix/breakfix/internal/adapter/incus"
	"github.com/breakfix/breakfix/internal/adapter/kubernetes"
	"github.com/breakfix/breakfix/internal/adapter/oci"
	"github.com/breakfix/breakfix/internal/adapter/opensandbox"
	"github.com/breakfix/breakfix/internal/adapter/postgres"
	appauthoring "github.com/breakfix/breakfix/internal/application/authoring"
	appgeneration "github.com/breakfix/breakfix/internal/application/generation"
	"github.com/breakfix/breakfix/internal/assistant"
	"github.com/breakfix/breakfix/internal/config"
	"github.com/breakfix/breakfix/internal/taxonomy"
)

// Handler owns the Server's shared dependencies. HTTP handlers are separated
// by domain so routing stays stable while each endpoint's responsibility is local.
type Handler struct {
	db                 *postgres.Store
	k8s                *kubernetes.Client
	authoring          *appauthoring.RuntimeService
	assistant          *assistant.Service
	registryAddr       string
	registryClient     oci.Client
	namespace          string
	crdNamespace       string
	challengesDir      string
	taxonomy           *taxonomy.Store
	taxonomyPublishMu  sync.Mutex
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
	generatorWorkspace *appgeneration.Manager
	runtimeConfig      config.RuntimeConfig
	incusConfig        incus.Config
	nodeTerminal       NodeTerminalProvider
	nodeProviderReady  NodeProviderReadiness
}

type Dependencies struct {
	NodeTerminal NodeTerminalProvider
}

func NewHandler(database *postgres.Store, client *kubernetes.Client, cfg config.Config) *Handler {
	return NewHandlerWithDependencies(database, client, cfg, Dependencies{})
}

func NewHandlerWithDependencies(database *postgres.Store, client *kubernetes.Client, cfg config.Config, dependencies Dependencies) *Handler {
	taxonomyStore := taxonomy.NewStore(cfg.DataDir)
	registryClient, registryErr := oci.NewClient(oci.ClientOptions{
		Endpoint:        cfg.Registry.ClientAddress,
		Credentials:     oci.Credentials{Username: cfg.Registry.Username, Password: cfg.Registry.Password},
		TrustBundleFile: cfg.Registry.TrustBundleFile,
	})
	handler := &Handler{
		db:              database,
		k8s:             client,
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
	}
	if database != nil {
		handler.authoring = appauthoring.NewRuntimeService(database.Authoring, cfg.Agent.Model)
		handler.assistant = assistant.NewService(database.Agent, cfg.Agent.Model)
	} else {
		handler.authoring = appauthoring.NewRuntimeService(nil, cfg.Agent.Model)
		handler.assistant = assistant.NewService(nil, cfg.Agent.Model)
	}
	if ready, ok := dependencies.NodeTerminal.(NodeProviderReadiness); ok {
		handler.nodeProviderReady = ready
	}
	if registryErr != nil {
		handler.startupErr = fmt.Errorf("initialize registry client: %w", registryErr)
		return handler
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
		manager, err := appgeneration.NewManager(database.Generation, client, sandbox, appgeneration.Config{
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
