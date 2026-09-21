package documentpractice

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"github.com/breakfix/breakfix/internal/domain/audit"
	domain "github.com/breakfix/breakfix/internal/domain/documentpractice"
)

// IgnitionDispatcher drives asynchronous Agent-chain ignitions for the HTTP
// endpoint. One chain runs minutes against the live model - and the
// gate-rejection auto retry re-runs it up to the max_revisions budget inside
// one chain - far beyond any sane HTTP request window. The ignition request
// therefore creates the workflow durably and acknowledges with its state,
// while the chain runs on a background goroutine, deduplicated per workflow,
// exactly like the batch scheduler's ignite. Callers observe progress through
// the workflow's durable state.
type IgnitionDispatcher struct {
	pipeline *AgentPipeline

	mu     sync.Mutex
	chains map[string]bool
}

func NewIgnitionDispatcher(pipeline *AgentPipeline) (*IgnitionDispatcher, error) {
	if pipeline == nil {
		return nil, errors.New("documentation ignition dispatcher requires the Agent pipeline")
	}
	return &IgnitionDispatcher{pipeline: pipeline, chains: map[string]bool{}}, nil
}

// Ignite durably creates (or re-observes) the workflow and, while it sits in
// Planning, drives one background Agent chain for it. A repeat request for a
// workflow whose chain is already live acknowledges the durable state without
// starting a second chain.
func (d *IgnitionDispatcher) Ignite(ctx context.Context, workflowID, pagePath, anchor string, ignition *audit.HumanAction) (domain.Workflow, error) {
	workflow, err := d.pipeline.Prepare(ctx, workflowID, pagePath, anchor, ignition)
	if err != nil {
		return domain.Workflow{}, err
	}
	if workflow.State == domain.Planning && d.launch(workflowID) {
		go func() {
			defer d.finish(workflowID)
			// The chain owns its own lifetime: the ignition request's context
			// dies with its acknowledgment, so the chain runs on the
			// background context like the batch scheduler's ignite.
			if _, err := d.pipeline.Start(context.Background(), workflowID, pagePath, anchor, nil); err != nil {
				slog.Warn("documentation practice chain ended without a durable outcome", "workflow_id", workflowID, "page_path", pagePath, "anchor", anchor, "err", err)
			}
		}()
	}
	return workflow, nil
}

// Live reports whether a background chain is currently driving the workflow.
func (d *IgnitionDispatcher) Live(workflowID string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.chains[workflowID]
}

func (d *IgnitionDispatcher) launch(workflowID string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.chains[workflowID] {
		return false
	}
	d.chains[workflowID] = true
	return true
}

func (d *IgnitionDispatcher) finish(workflowID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.chains, workflowID)
}
