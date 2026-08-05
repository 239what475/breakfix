package httpapi

import (
	"context"
	"fmt"
	"time"

	"github.com/breakfix/breakfix/internal/adapter/incus"
	"github.com/breakfix/breakfix/internal/adapter/kubernetes"
	"github.com/breakfix/breakfix/internal/adapter/postgres"
	appassistant "github.com/breakfix/breakfix/internal/application/assistant"
	appauthoring "github.com/breakfix/breakfix/internal/application/authoring"
	appcatalog "github.com/breakfix/breakfix/internal/application/catalog"
	approadmap "github.com/breakfix/breakfix/internal/application/roadmap"
	"github.com/breakfix/breakfix/internal/bootstrap/config"
)

// Handler owns the Server's shared dependencies. HTTP handlers are separated
// by domain so routing stays stable while each endpoint's responsibility is local.
type Handler struct {
	runtimeContext     context.Context
	db                 *postgres.Store
	k8s                *kubernetes.Client
	authoring          *appauthoring.RuntimeService
	catalog            *appcatalog.Service
	assistant          *appassistant.Service
	registryRepository string
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
	runtimeConfig      config.RuntimeConfig
	runtimeActions     runtimeClaimArbiter
	runtimeReaps       runtimeClaimArbiter
	incusConfig        incus.Config
	nodeTerminal       NodeTerminalProvider
	nodeProviderReady  NodeProviderReadiness
	generatorWorkspace GeneratorWorkspaceRetirer
	roadmapMaintenance *approadmap.MaintenanceService
}

// GeneratorWorkspaceRetirer is the sole authoring-facing lifecycle operation
// for a Server-owned Generator workspace. It cannot expose a sandbox or PVC.
type GeneratorWorkspaceRetirer interface {
	Retire(context.Context, string) error
}

type Dependencies struct {
	NodeTerminal       NodeTerminalProvider
	AssistantExecutor  appassistant.Executor
	AuthoringExecutor  appauthoring.Executor
	GeneratorWorkspace GeneratorWorkspaceRetirer
	RoadmapExecutor    approadmap.CommitteeExecutor
}

func NewHandlerWithDependencies(database *postgres.Store, client *kubernetes.Client, cfg config.Config, dependencies Dependencies) (*Handler, error) {
	var roadmap appcatalog.RoadmapStore
	var availability *appcatalog.Availability
	if database != nil {
		roadmap = database.Roadmap
		if cfg.Catalog.Enabled() {
			var err error
			availability, err = appcatalog.NewAvailability(cfg.Catalog.ReleaseReference, database.Catalog)
			if err != nil {
				return nil, fmt.Errorf("create catalog availability gate: %w", err)
			}
		}
	} else if cfg.Catalog.Enabled() {
		return nil, fmt.Errorf("configured catalog release requires a database")
	}
	handler := &Handler{
		runtimeContext:     context.Background(),
		db:                 database,
		k8s:                client,
		catalog:            appcatalog.NewService(cfg.ChallengesDir(), roadmap, availability),
		registryRepository: cfg.Registry.Repository,
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
		generatorWorkspace: dependencies.GeneratorWorkspace,
	}
	if database != nil {
		handler.authoring = appauthoring.NewRuntimeService(database.Authoring, cfg.Agent.Model, dependencies.AuthoringExecutor)
		handler.assistant = appassistant.NewService(database.Agent, cfg.Agent.Model, dependencies.AssistantExecutor)
		if dependencies.RoadmapExecutor != nil {
			maintenance, err := approadmap.NewMaintenanceService(approadmap.MaintenanceConfig{
				Repository: database.Roadmap, Executor: dependencies.RoadmapExecutor,
				ChallengeReader: approadmap.FilesystemChallengeReader{Root: cfg.ChallengesDir()},
				Model:           cfg.Agent.Model, ServerID: handler.serverInstance,
			})
			if err != nil {
				return nil, fmt.Errorf("create roadmap maintenance service: %w", err)
			}
			handler.roadmapMaintenance = maintenance
		}
	} else {
		handler.authoring = appauthoring.NewRuntimeService(nil, cfg.Agent.Model, dependencies.AuthoringExecutor)
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

func (h *Handler) setRuntimeContext(ctx context.Context) {
	if h == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	h.runtimeContext = ctx
}

func (h *Handler) agentRuntimeContext() context.Context {
	if h == nil || h.runtimeContext == nil {
		return context.Background()
	}
	return h.runtimeContext
}
