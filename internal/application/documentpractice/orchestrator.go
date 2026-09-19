package documentpractice

import (
	"errors"
	"strings"
	"sync"
	"time"

	domain "github.com/breakfix/breakfix/internal/domain/documentpractice"
)

type Orchestrator struct {
	mu       sync.Mutex
	ledger   *Ledger
	receipts map[string]domain.Workflow
}

func NewOrchestrator(ledger *Ledger) (*Orchestrator, error) {
	if ledger == nil {
		return nil, errors.New("document orchestrator requires a ledger")
	}
	return &Orchestrator{ledger: ledger, receipts: map[string]domain.Workflow{}}, nil
}

type TransitionRequest struct {
	WorkflowID           string
	IdempotencyKey       string
	ExpectedStateVersion int64
	Next                 domain.WorkflowState
	RequiredArtifacts    []string
}

func (o *Orchestrator) TransitionIdempotent(request TransitionRequest) (domain.Workflow, error) {
	if strings.TrimSpace(request.WorkflowID) == "" || strings.TrimSpace(request.IdempotencyKey) == "" {
		return domain.Workflow{}, errors.New("transition requires workflow and idempotency key")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if previous, ok := o.receipts[request.WorkflowID+":"+request.IdempotencyKey]; ok {
		return previous, nil
	}
	w, ok := o.ledger.Workflow(request.WorkflowID)
	if !ok {
		return domain.Workflow{}, errors.New("workflow not found")
	}
	if request.ExpectedStateVersion > 0 && w.StateVersion != request.ExpectedStateVersion {
		return domain.Workflow{}, errors.New("workflow state version is stale")
	}
	if err := w.AdvanceAt(request.Next, time.Now().UTC(), request.RequiredArtifacts...); err != nil {
		return domain.Workflow{}, err
	}
	o.ledger.mu.Lock()
	o.ledger.workflows[request.WorkflowID] = w
	o.ledger.mu.Unlock()
	o.receipts[request.WorkflowID+":"+request.IdempotencyKey] = w
	return w, nil
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
