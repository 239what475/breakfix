package server

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	docsource "github.com/breakfix/breakfix/internal/adapter/documentation"
	app "github.com/breakfix/breakfix/internal/application/documentpractice"
	"github.com/breakfix/breakfix/internal/domain/audit"
	domain "github.com/breakfix/breakfix/internal/domain/documentpractice"
)

// fixedDocumentationApplication exposes no page or Agent controls to HTTP.
// The source identity is deployment-owned; the ignition request addresses one
// page of the opened library.
type fixedDocumentationApplication struct {
	pipeline *app.AgentPipeline
	ignition *app.IgnitionDispatcher
	identity docsource.LibraryIdentity
}

func newFixedDocumentationApplication(pipeline *app.AgentPipeline, library *docsource.Library) (*fixedDocumentationApplication, error) {
	if pipeline == nil || library == nil {
		return nil, fmt.Errorf("documentation practice requires the Agent pipeline and the opened library")
	}
	ignition, err := app.NewIgnitionDispatcher(pipeline)
	if err != nil {
		return nil, err
	}
	context := library.PinnedContext()
	return &fixedDocumentationApplication{
		pipeline: pipeline,
		ignition: ignition,
		identity: docsource.LibraryIdentity{SourceID: context.SourceID, Repository: context.Repository, Commit: context.Commit, Version: context.Version, Language: context.Language, License: context.License},
	}, nil
}

// StartDocumentationPractice records which administrator pressed the ignition,
// for which page, together with the workflow creation, inside one durable
// transaction - and acknowledges immediately. The Agent chain runs for minutes
// against the live model; the dispatcher drives it in the background, so the
// caller observes progress through the workflow's durable state.
func (a *fixedDocumentationApplication) StartDocumentationPractice(ctx context.Context, actorID, pagePath, anchor string) (domain.Workflow, error) {
	if a == nil || a.ignition == nil {
		return domain.Workflow{}, fmt.Errorf("documentation practice is not configured")
	}
	context := domain.DocumentContext{
		FormatVersion: domain.FormatVersion, SourceID: a.identity.SourceID, Repository: a.identity.Repository,
		Commit: a.identity.Commit, Version: a.identity.Version, Language: a.identity.Language, License: a.identity.License,
		PagePath: pagePath, Anchor: anchor,
	}
	if err := context.Validate(); err != nil {
		return domain.Workflow{}, fmt.Errorf("documentation practice request is invalid: %w", err)
	}
	workflowID := "document-workflow-" + domain.ContentID(context)
	now := time.Now().UTC()
	detail, err := json.Marshal(map[string]string{"workflow_id": workflowID, "page_path": pagePath, "anchor": anchor})
	if err != nil {
		return domain.Workflow{}, err
	}
	action := audit.HumanAction{
		ID:         audit.NewID(now),
		UserID:     actorID,
		Action:     audit.ActionDocumentationPracticeStart,
		TargetType: audit.TargetDocumentWorkflow,
		TargetID:   workflowID,
		Detail:     detail,
		CreatedAt:  now,
	}
	return a.ignition.Ignite(ctx, workflowID, pagePath, anchor, &action)
}

// ForceFailDocumentationWorkflow resolves a stuck workflow as an
// administrative decision and records the acting admin.
func (a *fixedDocumentationApplication) ForceFailDocumentationWorkflow(ctx context.Context, workflowID, reason string, action *audit.HumanAction) (domain.Workflow, error) {
	if a == nil || a.pipeline == nil {
		return domain.Workflow{}, fmt.Errorf("documentation practice is not configured")
	}
	return a.pipeline.ForceFail(ctx, workflowID, reason, action)
}

// RestartDocumentationWorkflow resets a failed or rejected workflow to
// Planning and records the acting admin; the next ignition re-runs the Agent.
func (a *fixedDocumentationApplication) RestartDocumentationWorkflow(ctx context.Context, workflowID, reason string, action *audit.HumanAction) (domain.Workflow, error) {
	if a == nil || a.pipeline == nil {
		return domain.Workflow{}, fmt.Errorf("documentation practice is not configured")
	}
	return a.pipeline.Restart(ctx, workflowID, reason, action)
}
