package server

import (
	"context"
	"fmt"

	app "github.com/breakfix/breakfix/internal/application/documentpractice"
	"github.com/breakfix/breakfix/internal/bootstrap/config"
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

func (a *fixedDocumentationApplication) StartDocumentationPractice(ctx context.Context) (domain.Workflow, error) {
	if a == nil || a.pipeline == nil {
		return domain.Workflow{}, fmt.Errorf("documentation practice is not configured")
	}
	result, err := a.pipeline.Start(ctx, a.workflowID, a.pagePath, a.anchor)
	if err != nil {
		return domain.Workflow{}, err
	}
	return result.Workflow, nil
}
