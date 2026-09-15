package documentpractice

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	domain "github.com/breakfix/breakfix/internal/domain/documentpractice"
)

type Orchestrator struct {
	mu     sync.Mutex
	ledger *Ledger
}

func NewOrchestrator(ledger *Ledger) (*Orchestrator, error) {
	if ledger == nil {
		return nil, errors.New("document orchestrator requires a ledger")
	}
	return &Orchestrator{ledger: ledger}, nil
}

func (o *Orchestrator) Transition(workflowID string, next domain.WorkflowState, requiredArtifacts ...string) (domain.Workflow, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	w, ok := o.ledger.Workflow(workflowID)
	if !ok {
		return domain.Workflow{}, errors.New("workflow not found")
	}
	if err := w.AdvanceAt(next, time.Now().UTC(), requiredArtifacts...); err != nil {
		return domain.Workflow{}, err
	}
	o.ledger.mu.Lock()
	o.ledger.workflows[workflowID] = w
	o.ledger.mu.Unlock()
	return w, nil
}

func (o *Orchestrator) Append(workflowID string, artifact domain.ArtifactRecord) error {
	return o.ledger.Append(workflowID, artifact, time.Now().UTC())
}

func (o *Orchestrator) Start(id string) (domain.Workflow, error) {
	w, err := domain.NewWorkflow(id, time.Now().UTC())
	if err != nil {
		return domain.Workflow{}, err
	}
	if err := o.ledger.PutWorkflow(w); err != nil {
		return domain.Workflow{}, err
	}
	return w, nil
}

func (o *Orchestrator) RequireLease(workflowID, owner string, now time.Time) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if strings.TrimSpace(owner) == "" || now.IsZero() {
		return errors.New("workflow lease requires owner and time")
	}
	w, ok := o.ledger.Workflow(workflowID)
	if !ok {
		return errors.New("workflow not found")
	}
	if w.LeaseOwner != "" && w.LeaseOwner != owner && w.LeaseExpiresAt != nil && w.LeaseExpiresAt.After(now) {
		return fmt.Errorf("workflow lease held by %s", w.LeaseOwner)
	}
	expires := now.Add(2 * time.Minute)
	w.LeaseOwner, w.LeaseExpiresAt = owner, &expires
	o.ledger.mu.Lock()
	o.ledger.workflows[workflowID] = w
	o.ledger.mu.Unlock()
	return nil
}
