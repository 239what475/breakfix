package operations

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/content/scenario"
	"github.com/breakfix/breakfix/internal/domain/runnable"
	scenariodomain "github.com/breakfix/breakfix/internal/domain/scenario"
)

// MaterializationStore is the public runnable persistence boundary used by
// Operations publication. The Worker remains the only component that creates
// the provider artifact; this service only freezes inputs and schedules work.
type MaterializationStore interface {
	StoreRunnableSource(context.Context, runnable.SourceArchive, []byte, time.Time) error
	ScheduleMaterialization(context.Context, runnable.RunnableSpec, int64, time.Time) (runnable.ActionIdentity, error)
	ResolveMaterializedRunnableRevision(context.Context, runnable.ActionIdentity) (runnable.RevisionReference, error)
	// PublishOperationsRevision is the one-way, idempotent transaction that
	// makes a completed public RunnableRevision visible to Operations. The
	// application service owns when publication is attempted; persistence owns
	// the revision/binding transaction and conflict fence.
	PublishOperationsRevision(context.Context, string, string, runnable.RevisionReference, time.Time) error
}

type RevisionSource interface {
	GetScenarioRevision(context.Context, string, string) (*scenariodomain.Revision, error)
}

type PublishedRevision struct {
	ScenarioID         string
	ScenarioRevisionID string
	ContentRevision    string
	Root               string
	Entry              scenario.Entry
}

func (p PublishedRevision) Validate() error {
	if err := scenario.RequireOperationsScenario(&p.Entry); err != nil {
		return err
	}
	if !scenario.ValidID(p.ScenarioID) || !scenario.ValidRevisionID(p.ScenarioRevisionID) ||
		!scenario.ValidRevision(p.ContentRevision) || strings.TrimSpace(p.Root) == "" {
		return errors.New("published Operations revision identity is invalid")
	}
	if p.Entry.ID != p.ScenarioID || p.Entry.RevisionID != p.ScenarioRevisionID ||
		!scenario.ValidRevisionID(p.Entry.RevisionID) || p.Entry.ContentRevision != p.ContentRevision {
		return errors.New("published Operations revision does not match its materialized entry")
	}
	return nil
}

type PreparedPublication struct {
	Revision PublishedRevision
	Spec     runnable.RunnableSpec
	Action   runnable.ActionIdentity
	Source   runnable.SourceArchive
}

// Publisher freezes an already published Operations directory into the
// generic runnable pipeline. It deliberately has no provider or filesystem
// publication side effects beyond reading the immutable source tree.
type Publisher struct {
	store  MaterializationStore
	source RevisionSource
	root   string
	config Config
	now    func() time.Time
}

func NewPublisher(store MaterializationStore, config Config) (*Publisher, error) {
	if store == nil {
		return nil, errors.New("Operations publisher requires a materialization store")
	}
	return &Publisher{store: store, config: config, now: func() time.Time { return time.Now().UTC() }}, nil
}

// NewRevisionPublisher adds the durable Scenario lookup used by the Server
// publication loop. The simpler NewPublisher remains useful for callers that
// already hold a validated immutable revision.
func NewRevisionPublisher(store MaterializationStore, source RevisionSource, scenariosRoot string, config Config) (*Publisher, error) {
	if source == nil || strings.TrimSpace(scenariosRoot) == "" {
		return nil, errors.New("Operations revision publisher requires a revision source and scenarios root")
	}
	publisher, err := NewPublisher(store, config)
	if err != nil {
		return nil, err
	}
	publisher.source = source
	publisher.root = filepath.Clean(scenariosRoot)
	return publisher, nil
}

func (p *Publisher) PrepareRevision(ctx context.Context, scenarioID, revisionID string, stateVersion int64, now time.Time) (*PreparedPublication, error) {
	if p == nil || p.source == nil || p.root == "" || !scenario.ValidID(scenarioID) || !scenario.ValidRevisionID(revisionID) {
		return nil, errors.New("prepare Operations revision requires a durable source and valid identity")
	}
	durable, err := p.source.GetScenarioRevision(ctx, scenarioID, revisionID)
	if err != nil {
		return nil, err
	}
	if durable == nil || durable.Type != scenario.ScenarioOperationsScenario {
		return nil, errors.New("durable revision is not an Operations scenario")
	}
	root := filepath.Join(p.root, filepath.FromSlash(durable.MaterializedPath))
	entry, err := scenario.ValidateDir(root)
	if err != nil {
		return nil, fmt.Errorf("validate materialized Operations revision: %w", err)
	}
	return p.Prepare(ctx, PublishedRevision{ScenarioID: scenarioID, ScenarioRevisionID: revisionID, ContentRevision: durable.ContentRevision, Root: root, Entry: *entry}, stateVersion, now)
}

func (p *Publisher) Prepare(ctx context.Context, revision PublishedRevision, stateVersion int64, now time.Time) (*PreparedPublication, error) {
	if p == nil || p.store == nil {
		return nil, errors.New("Operations publisher is not configured")
	}
	if err := revision.Validate(); err != nil || stateVersion < 1 || now.IsZero() {
		return nil, fmt.Errorf("prepare Operations publication: %w", err)
	}
	source, archive, err := BuildSourceArchive(revision.Root)
	if err != nil {
		return nil, err
	}
	spec, err := Compile(Input{ContentID: revision.ScenarioID, ContentRevision: revision.ContentRevision, Entry: revision.Entry, Source: source}, p.config)
	if err != nil {
		return nil, err
	}
	if err := p.store.StoreRunnableSource(ctx, source, archive, now.UTC()); err != nil {
		return nil, fmt.Errorf("store Operations runnable source: %w", err)
	}
	action, err := p.store.ScheduleMaterialization(ctx, spec, stateVersion, now.UTC())
	if err != nil {
		return nil, fmt.Errorf("schedule Operations runnable materialization: %w", err)
	}
	return &PreparedPublication{Revision: revision, Spec: spec, Action: action, Source: source}, nil
}

// Finalize binds the exact immutable revision returned by the materialization
// action. It is safe to retry after a Server restart and never accepts a
// caller-constructed artifact or digest.
func (p *Publisher) Finalize(ctx context.Context, prepared PreparedPublication, now time.Time) (runnable.RevisionReference, error) {
	if p == nil || p.store == nil || now.IsZero() {
		return runnable.RevisionReference{}, errors.New("finalize Operations publication is invalid")
	}
	if err := prepared.Revision.Validate(); err != nil {
		return runnable.RevisionReference{}, err
	}
	if err := prepared.Spec.Validate(); err != nil {
		return runnable.RevisionReference{}, err
	}
	if prepared.Spec.Identity.ID != prepared.Revision.ScenarioID || prepared.Spec.Identity.Revision != prepared.Revision.ContentRevision || prepared.Action.Phase != runnable.ActionMaterializeArtifact {
		return runnable.RevisionReference{}, errors.New("prepared Operations publication identity is inconsistent")
	}
	reference, err := p.store.ResolveMaterializedRunnableRevision(ctx, prepared.Action)
	if err != nil {
		return runnable.RevisionReference{}, err
	}
	if err := p.store.PublishOperationsRevision(ctx, prepared.Revision.ScenarioID, prepared.Revision.ScenarioRevisionID, reference, now.UTC()); err != nil {
		return runnable.RevisionReference{}, fmt.Errorf("publish Operations runnable revision: %w", err)
	}
	return reference, nil
}
