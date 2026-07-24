package server

import (
	"time"

	"github.com/breakfix/breakfix/internal/assistant"
	"github.com/breakfix/breakfix/internal/authoring"
	"github.com/breakfix/breakfix/internal/config"
	"github.com/breakfix/breakfix/internal/db"
	"github.com/breakfix/breakfix/internal/k8s"
)

// Handler owns the Server's shared dependencies. HTTP handlers are separated
// by domain so routing stays stable while each endpoint's responsibility is local.
type Handler struct {
	db               *db.DB
	k8s              *k8s.Client
	authoring        *authoring.Service
	assistant        *assistant.Service
	registryAddr     string
	registryInsecure bool
	namespace        string
	crdNamespace     string
	challengesDir    string
	dataDir          string
	cooldownMin      int
	llm              config.LLMConfig
	jwtSecret        []byte
	internalAPIKey   string
	serverHost       string
	port             int
	terminals        *terminalConnectionTracker
	serverInstance   string
}

func NewHandler(database *db.DB, client *k8s.Client, cfg config.Config) *Handler {
	return &Handler{
		db:               database,
		k8s:              client,
		authoring:        authoring.NewService(database, cfg.LLM),
		assistant:        assistant.NewService(database, cfg.LLM),
		registryAddr:     cfg.RegistryAddr,
		registryInsecure: cfg.RegistryInsecure,
		namespace:        cfg.Namespace,
		crdNamespace:     cfg.CRDNamespace,
		challengesDir:    cfg.ChallengesDir(),
		dataDir:          cfg.DataDir,
		cooldownMin:      cfg.CooldownMinutes,
		llm:              cfg.LLM,
		jwtSecret:        []byte(cfg.JWTSecret),
		internalAPIKey:   cfg.InternalAPIKey,
		serverHost:       cfg.ServerHost,
		port:             cfg.Port,
		terminals:        newTerminalConnectionTracker(time.Second),
		serverInstance:   newServerInstanceID(),
	}
}
