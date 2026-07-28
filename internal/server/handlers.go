package server

import (
	"time"

	"github.com/breakfix/breakfix/internal/assistant"
	"github.com/breakfix/breakfix/internal/authoring"
	"github.com/breakfix/breakfix/internal/config"
	"github.com/breakfix/breakfix/internal/db"
	"github.com/breakfix/breakfix/internal/k8s"
	"github.com/breakfix/breakfix/internal/opensandbox"
	"github.com/breakfix/breakfix/internal/registry"
	"github.com/breakfix/breakfix/internal/taxonomy"
	"github.com/breakfix/breakfix/internal/workspace"
)

// Handler owns the Server's shared dependencies. HTTP handlers are separated
// by domain so routing stays stable while each endpoint's responsibility is local.
type Handler struct {
	db                   *db.DB
	k8s                  *k8s.Client
	authoring            *authoring.RuntimeService
	assistant            *assistant.Service
	registryAddr         string
	registryInsecure     bool
	namespace            string
	crdNamespace         string
	challengesDir        string
	taxonomy             *taxonomy.Store
	taxonomyWorkflow     *taxonomy.Service
	dataDir              string
	cooldownMin          int
	llm                  config.AgentConfig
	jwtSecret            []byte
	internalAPIKey       string
	verificationGrantKey []byte
	registryCredentials  registry.Credentials
	serverHost           string
	port                 int
	terminals            *terminalConnectionTracker
	serverInstance       string
	generatorSandbox     *opensandbox.Client
	generatorWorkspace   *workspace.Manager
}

func NewHandler(database *db.DB, client *k8s.Client, cfg config.Config) *Handler {
	taxonomyStore := taxonomy.NewStore(cfg.DataDir)
	handler := &Handler{
		db:                   database,
		k8s:                  client,
		authoring:            authoring.NewRuntimeService(database, cfg.Agent.Model),
		assistant:            assistant.NewService(database, cfg.Agent.Model),
		registryAddr:         cfg.RegistryAddr,
		registryInsecure:     cfg.RegistryInsecure,
		namespace:            cfg.Namespace,
		crdNamespace:         cfg.CRDNamespace,
		challengesDir:        cfg.ChallengesDir(),
		taxonomy:             taxonomyStore,
		dataDir:              cfg.DataDir,
		cooldownMin:          cfg.CooldownMinutes,
		llm:                  cfg.Agent,
		jwtSecret:            []byte(cfg.JWTSecret),
		internalAPIKey:       cfg.InternalAPIKey,
		verificationGrantKey: []byte(cfg.VerificationGrantKey),
		registryCredentials:  registry.Credentials{Username: cfg.RegistryUsername, Password: cfg.RegistryPassword},
		serverHost:           cfg.ServerHost,
		port:                 cfg.Port,
		terminals:            newTerminalConnectionTracker(time.Second),
		serverInstance:       newServerInstanceID(),
	}
	if database != nil {
		handler.taxonomyWorkflow = taxonomy.NewService(database, taxonomyStore, cfg.ChallengesDir(), cfg.Agent)
	}
	if database != nil && client != nil && cfg.OpenSandbox.APIKey != "" {
		if sandbox, err := opensandbox.New(cfg.OpenSandbox); err == nil {
			if manager, managerErr := workspace.NewManager(database, client, sandbox, workspace.Config{
				Namespace: cfg.OpenSandbox.Namespace,
				Storage:   cfg.OpenSandbox.WorkspaceStorage,
			}); managerErr == nil {
				handler.generatorSandbox = sandbox
				handler.generatorWorkspace = manager
			}
		}
	}
	return handler
}
