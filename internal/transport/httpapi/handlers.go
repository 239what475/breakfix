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
	appgeneration "github.com/breakfix/breakfix/internal/application/generation"
	"github.com/breakfix/breakfix/internal/bootstrap/config"
	"github.com/breakfix/breakfix/internal/domain/audit"
	authoringdomain "github.com/breakfix/breakfix/internal/domain/authoring"
	documentdomain "github.com/breakfix/breakfix/internal/domain/documentpractice"
	generationdomain "github.com/breakfix/breakfix/internal/domain/generation"
	"github.com/breakfix/breakfix/internal/domain/runnable"
	"github.com/breakfix/breakfix/internal/domain/toolresult"
)

// Handler owns the Server's shared dependencies. HTTP handlers are separated
// by domain so routing stays stable while each endpoint's responsibility is local.
type Handler struct {
	runtimeContext       context.Context
	db                   *postgres.Store
	k8s                  *kubernetes.Client
	runnableBindings     operationsRunnableBindingResolver
	authoring            *appauthoring.RuntimeService
	catalog              *appcatalog.Service
	assistant            *appassistant.Service
	registryRepository   string
	namespace            string
	crdNamespace         string
	scenariosDir         string
	dataDir              string
	cooldownMin          int
	llm                  config.AgentConfig
	jwtSecret            []byte
	internalWorkers      config.InternalWorkerKeys
	port                 int
	uiOrigin             string
	terminals            *terminalConnectionTracker
	serverInstance       string
	allowRegistration    bool
	agentStuckAfter      time.Duration
	runtimeConfig        config.RuntimeConfig
	incusConfig          incus.Config
	nodeTerminal         NodeTerminalProvider
	nodeProviderReady    NodeProviderReadiness
	generator            generatorApplication
	documentation        documentationApplication
	documentationBatches documentationBatchApplication
	documentationLibrary documentationLibrary
	documentationReader  documentationPracticeReader
	systemReport         SystemReportProvider
}

// generatorApplication is the HTTP consumer's view of GeneratorService. The
// transport boundary receives only safe application operations, never a
// Sandbox, PVC, archive path, or provider lifecycle dependency.
type generatorApplication interface {
	SetGenerationPlan(context.Context, string, string, int64, string, authoringdomain.Plan) (*authoringdomain.Session, *authoringdomain.Revision, error)
	ConfirmGeneration(context.Context, string, string, generationdomain.StartConfirmation) (*generationdomain.Workflow, error)
	GetGeneration(context.Context, string, string) (*appgeneration.GenerationView, error)
	GetGenerationWorkflow(context.Context, string, string) (*generationdomain.Workflow, error)
	ListActiveGenerations(context.Context, string) ([]generationdomain.Workflow, error)
	StartWorkspaceTurn(context.Context, string, generationdomain.WorkspaceTurn) error
	EndWorkspaceTurn(context.Context, string, generationdomain.WorkspaceTurn) error
	ListWorkspaceFiles(context.Context, string, generationdomain.WorkspaceTurn) ([]generationdomain.WorkspaceFile, error)
	ReadWorkspaceFile(context.Context, string, generationdomain.WorkspaceTurn, string, int, int) (appgeneration.FileReadResponse, error)
	WriteWorkspaceFile(context.Context, string, generationdomain.WorkspaceTurn, string, string) error
	ExecuteWorkspaceCommand(context.Context, string, generationdomain.WorkspaceTurn, string) (toolresult.Envelope, error)
	SubmitCandidate(context.Context, string, generationdomain.CandidateSubmission) (*generationdomain.Revision, error)
	ConfirmContent(context.Context, string, generationdomain.ContentConfirmation) (*generationdomain.Workflow, error)
	RequestContentChanges(context.Context, string, generationdomain.ContentChangeRequest) (*generationdomain.Workflow, error)
	CancelGeneration(context.Context, string, generationdomain.Cancellation) (*generationdomain.Workflow, error)
}

// documentationApplication starts only the Server-configured fixed workflow
// and carries the administrative force-fail and restart verbs. It has no
// endpoint for Agent artifacts, arbitrary pages, or runtime policy.
type documentationApplication interface {
	StartDocumentationPractice(context.Context, string, string, string) (documentdomain.Workflow, error)
	ForceFailDocumentationWorkflow(context.Context, string, string, *audit.HumanAction) (documentdomain.Workflow, error)
	RestartDocumentationWorkflow(context.Context, string, string, *audit.HumanAction) (documentdomain.Workflow, error)
}

type Dependencies struct {
	NodeTerminal         NodeTerminalProvider
	Assistant            *appassistant.Service
	Authoring            *appauthoring.RuntimeService
	Catalog              *appcatalog.Service
	AgentRuntimeContext  context.Context
	Generator            generatorApplication
	RunnableBindings     operationsRunnableBindingResolver
	Documentation        documentationApplication
	DocumentationBatches documentationBatchApplication
	DocumentationLibrary documentationLibrary
	DocumentationReader  documentationPracticeReader
	// SystemReport assembles the admin system status from process-scoped state
	// that only the bootstrap owns: build information and the background
	// service registry.
	SystemReport SystemReportProvider
}

// operationsRunnableBindingResolver prevents Server-created environments from
// reconstructing a runtime profile or artifact from mutable content data.
type operationsRunnableBindingResolver interface {
	ResolveOperationsRevisionBinding(context.Context, string) (runnable.RevisionReference, error)
}

// documentationPracticeReader is the reader-facing port over published
// practices: the per-page index, one reader-visible revision, and the
// immutable runnable revision its runtime summary resolves through.
type documentationPracticeReader interface {
	ListPublishedPracticesForPage(ctx context.Context, sourceID, commit, language, pagePath string) ([]postgres.PublishedPracticeSummary, error)
	GetPublishedPractice(ctx context.Context, practiceID string) (documentdomain.PracticeRevision, error)
	ResolveRunnableRevision(ctx context.Context, id, digest string) (runnable.RunnableRevision, error)
}

func NewHandlerWithDependencies(database *postgres.Store, client *kubernetes.Client, cfg config.Config, dependencies Dependencies) (*Handler, error) {
	agentStuckAfter, err := cfg.AgentStuckDuration()
	if err != nil {
		return nil, err
	}
	catalogService := dependencies.Catalog
	if catalogService == nil {
		var lifecycle appcatalog.ScenarioLifecycleStore
		var availability *appcatalog.Availability
		if database != nil {
			lifecycle = database.Scenario
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
		var runnable appcatalog.RunnableRevisionResolver
		if database != nil {
			runnable = database.Runnable
		}
		catalogService = appcatalog.NewService(cfg.ScenariosDir(), availability, lifecycle, runnable)
	}
	agentRuntimeContext := dependencies.AgentRuntimeContext
	if agentRuntimeContext == nil {
		agentRuntimeContext = context.Background()
	}
	handler := &Handler{
		runtimeContext:       agentRuntimeContext,
		db:                   database,
		k8s:                  client,
		runnableBindings:     dependencies.RunnableBindings,
		catalog:              catalogService,
		registryRepository:   cfg.Registry.Repository,
		namespace:            cfg.Namespace,
		crdNamespace:         cfg.CRDNamespace,
		scenariosDir:         cfg.ScenariosDir(),
		dataDir:              cfg.DataDir,
		cooldownMin:          cfg.CooldownMinutes,
		llm:                  cfg.Agent,
		jwtSecret:            []byte(cfg.JWTSecret),
		internalWorkers:      cfg.InternalWorkers,
		port:                 cfg.Port,
		uiOrigin:             cfg.UIOrigin,
		terminals:            newTerminalConnectionTracker(time.Second),
		serverInstance:       newServerInstanceID(),
		allowRegistration:    cfg.AllowRegistration,
		agentStuckAfter:      agentStuckAfter,
		runtimeConfig:        cfg.Runtime,
		incusConfig:          cfg.Incus,
		nodeTerminal:         dependencies.NodeTerminal,
		generator:            dependencies.Generator,
		documentation:        dependencies.Documentation,
		documentationBatches: dependencies.DocumentationBatches,
		documentationLibrary: dependencies.DocumentationLibrary,
		documentationReader:  dependencies.DocumentationReader,
		systemReport:         dependencies.SystemReport,
	}
	if handler.runnableBindings == nil && database != nil {
		handler.runnableBindings = database.Runnable
	}
	if handler.documentationReader == nil && database != nil {
		handler.documentationReader = postgresPracticeReader{store: database}
	}
	handler.authoring = dependencies.Authoring
	handler.assistant = dependencies.Assistant
	if handler.authoring == nil {
		deadline, err := cfg.Agent.AuthoringDeadline()
		if err != nil {
			return nil, fmt.Errorf("parse authoring run deadline: %w", err)
		}
		if database != nil {
			handler.authoring = appauthoring.NewRuntimeService(database.Authoring, cfg.Agent.Model, deadline, nil)
		} else {
			handler.authoring = appauthoring.NewRuntimeService(nil, cfg.Agent.Model, deadline, nil)
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
