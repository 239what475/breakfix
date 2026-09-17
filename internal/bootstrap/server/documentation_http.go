package server

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	app "github.com/breakfix/breakfix/internal/application/documentpractice"
	"github.com/breakfix/breakfix/internal/bootstrap/config"
	"github.com/breakfix/breakfix/internal/domain/audit"
	domain "github.com/breakfix/breakfix/internal/domain/documentpractice"
)

// fixedDocumentationApplication exposes no page or Agent controls to HTTP.
// The source location and workflow identity are entirely deployment-owned.
type fixedDocumentationApplication struct {
	pipeline   *app.AgentPipeline
	workflowID string
	pagePath   string
	anchor     string
}

func newFixedDocumentationApplication(pipeline *app.AgentPipeline, cfg config.DocumentationConfig) *fixedDocumentationApplication {
	if pipeline == nil || !cfg.Enabled() {
		return nil
	}
	context := domain.DocumentContext{SourceID: cfg.SourceID, Commit: cfg.Revision, Language: cfg.Language, PagePath: cfg.PagePath, Anchor: cfg.Anchor}
	return &fixedDocumentationApplication{
		pipeline: pipeline, workflowID: "document-workflow-" + domain.ContentID(context),
		pagePath: cfg.PagePath, anchor: cfg.Anchor,
	}
}

// StartDocumentationPractice records which administrator pressed the ignition
// together with the workflow creation, inside one durable transaction.
func (a *fixedDocumentationApplication) StartDocumentationPractice(ctx context.Context, actorID string) (domain.Workflow, error) {
	if a == nil || a.pipeline == nil {
		return domain.Workflow{}, fmt.Errorf("documentation practice is not configured")
	}
	now := time.Now().UTC()
	detail, err := json.Marshal(map[string]string{"workflow_id": a.workflowID})
	if err != nil {
		return domain.Workflow{}, err
	}
	action := audit.HumanAction{
		ID:         audit.NewID(now),
		UserID:     actorID,
		Action:     audit.ActionDocumentationPracticeStart,
		TargetType: audit.TargetDocumentWorkflow,
		TargetID:   a.workflowID,
		Detail:     detail,
		CreatedAt:  now,
	}
	result, err := a.pipeline.Start(ctx, a.workflowID, a.pagePath, a.anchor, &action)
	if err != nil {
		return domain.Workflow{}, err
	}
	return result.Workflow, nil
}
