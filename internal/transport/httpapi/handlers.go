package httpapi

import (
	"time"

	"github.com/breakfix/breakfix/internal/adapter/incus"
	"github.com/breakfix/breakfix/internal/adapter/kubernetes"
	"github.com/breakfix/breakfix/internal/adapter/oci"
	"github.com/breakfix/breakfix/internal/adapter/opensandbox"
	"github.com/breakfix/breakfix/internal/adapter/postgres"
	appassistant "github.com/breakfix/breakfix/internal/application/assistant"
	appauthoring "github.com/breakfix/breakfix/internal/application/authoring"
	appcatalog "github.com/breakfix/breakfix/internal/application/catalog"
	appgeneration "github.com/breakfix/breakfix/internal/application/generation"
	"github.com/breakfix/breakfix/internal/bootstrap/config"
)

// Handler owns the Server's shared dependencies. HTTP handlers are separated
// by domain so routing stays stable while each endpoint's responsibility is local.
type Handler struct {
	db                 *postgres.Store
	k8s                *kubernetes.Client
	authoring          *appauthoring.RuntimeService
	catalog            *appcatalog.Service
	assistant          *appassistant.Service
	registryRepository string
	registryClient     oci.Client
	namespace          string
	crdNamespace       string
	challengesDir      string
	dataDir            string
	cooldownMin        int
	llm                config.AgentConfig
	jwtSecret          []byte
	internalWorkers    config.InternalWorkerKeys
	port               int
	uiOrigin           string
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
	NodeTerminal       NodeTerminalProvider
	AssistantExecutor  appassistant.Executor
	AuthoringExecutor  appauthoring.Executor
	RegistryClient     oci.Client
	GeneratorSandbox   *opensandbox.Client
	GeneratorWorkspace *appgeneration.Manager
}

func NewHandlerWithDependencies(database *postgres.Store, client *kubernetes.Client, cfg config.Config, dependencies Dependencies) (*Handler, error) {
	var roadmap appcatalog.RoadmapStore
	if database != nil {
		roadmap = database.Roadmap
	}
	handler := &Handler{
		db:                 database,
		k8s:                client,
		catalog:            appcatalog.NewService(cfg.ChallengesDir(), roadmap),
		registryRepository: cfg.Registry.Repository,
		registryClient:     dependencies.RegistryClient,
		namespace:          cfg.Namespace,
		crdNamespace:       cfg.CRDNamespace,
		challengesDir:      cfg.ChallengesDir(),
		dataDir:            cfg.DataDir,
		cooldownMin:        cfg.CooldownMinutes,
		llm:                cfg.Agent,
		jwtSecret:          []byte(cfg.JWTSecret),
		internalWorkers:    cfg.InternalWorkers,
		port:               cfg.Port,
		uiOrigin:           cfg.UIOrigin,
		terminals:          newTerminalConnectionTracker(time.Second),
		serverInstance:     newServerInstanceID(),
		runtimeConfig:      cfg.Runtime,
		incusConfig:        cfg.Incus,
		nodeTerminal:       dependencies.NodeTerminal,
		generatorSandbox:   dependencies.GeneratorSandbox,
		generatorWorkspace: dependencies.GeneratorWorkspace,
	}
	if database != nil {
		handler.authoring = appauthoring.NewRuntimeService(database.Authoring, database.Agent, cfg.Agent.Model, dependencies.AuthoringExecutor)
		handler.assistant = appassistant.NewService(database.Agent, cfg.Agent.Model, dependencies.AssistantExecutor)
	} else {
		handler.authoring = appauthoring.NewRuntimeService(nil, nil, cfg.Agent.Model, dependencies.AuthoringExecutor)
		handler.assistant = appassistant.NewService(nil, cfg.Agent.Model, dependencies.AssistantExecutor)
	}
	if ready, ok := dependencies.NodeTerminal.(NodeProviderReadiness); ok {
		handler.nodeProviderReady = ready
	}
	if database == nil {
		return handler, nil
	}
	return handler, nil
}
