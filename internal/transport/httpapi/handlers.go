package httpapi

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/adapter/incus"
	"github.com/breakfix/breakfix/internal/adapter/kubernetes"
	"github.com/breakfix/breakfix/internal/adapter/postgres"
	appassistant "github.com/breakfix/breakfix/internal/application/assistant"
	appauthoring "github.com/breakfix/breakfix/internal/application/authoring"
	appcatalog "github.com/breakfix/breakfix/internal/application/catalog"
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
}

// GeneratorWorkspaceRetirer is the sole authoring-facing lifecycle operation
// for a Server-owned Generator workspace. It cannot expose a sandbox or PVC.
type GeneratorWorkspaceRetirer interface {
	Retire(context.Context, string) error
}

type Dependencies struct {
	NodeTerminal        NodeTerminalProvider
	Assistant           *appassistant.Service
	Authoring           *appauthoring.RuntimeService
	Catalog             *appcatalog.Service
	AgentRuntimeContext context.Context
	GeneratorWorkspace  GeneratorWorkspaceRetirer
}

func NewHandlerWithDependencies(database *postgres.Store, client *kubernetes.Client, cfg config.Config, dependencies Dependencies) (*Handler, error) {
	catalogService := dependencies.Catalog
	if catalogService == nil {
		var roadmap appcatalog.RoadmapStore
		var lifecycle appcatalog.ChallengeLifecycleStore
		var availability *appcatalog.Availability
		if database != nil {
			roadmap = database.Roadmap
			lifecycle = database.Challenge
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
		catalogService = appcatalog.NewService(cfg.ChallengesDir(), roadmap, availability, lifecycle)
	}
	agentRuntimeContext := dependencies.AgentRuntimeContext
	if agentRuntimeContext == nil {
		agentRuntimeContext = context.Background()
	}
	handler := &Handler{
		runtimeContext:     agentRuntimeContext,
		db:                 database,
		k8s:                client,
		catalog:            catalogService,
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
	handler.authoring = dependencies.Authoring
	handler.assistant = dependencies.Assistant
	if handler.authoring == nil {
		if database != nil {
			handler.authoring = appauthoring.NewRuntimeService(database.Authoring, cfg.Agent.Model, nil)
		} else {
			handler.authoring = appauthoring.NewRuntimeService(nil, cfg.Agent.Model, nil)
		}
	}
	if handler.assistant == nil {
		if database != nil {
			handler.assistant = appassistant.NewService(database.Agent, cfg.Agent.Model, nil)
		} else {
			handler.assistant = appassistant.NewService(nil, cfg.Agent.Model, nil)
		}
	}
	if ready, ok := dependencies.NodeTerminal.(NodeProviderReadiness); ok {
		handler.nodeProviderReady = ready
	}
	if database == nil {
		return handler, nil
	}
	return handler, nil
}

func (h *Handler) agentRuntimeContext() context.Context {
	if h == nil || h.runtimeContext == nil {
		return context.Background()
	}
	return h.runtimeContext
}

func newServerInstanceID() string {
	hostname, err := os.Hostname()
	if err != nil || strings.TrimSpace(hostname) == "" {
		hostname = "server"
	}
	return fmt.Sprintf("%s-%d", hostname, time.Now().UnixNano())
}
