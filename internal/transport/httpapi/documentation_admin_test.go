package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/adapter/postgres"
	"github.com/breakfix/breakfix/internal/adapter/kubernetes"
	appdocument "github.com/breakfix/breakfix/internal/application/documentpractice"
	"github.com/breakfix/breakfix/internal/bootstrap/config"
	"github.com/breakfix/breakfix/internal/domain/audit"
	documentdomain "github.com/breakfix/breakfix/internal/domain/documentpractice"
	api "github.com/breakfix/breakfix/internal/transport/httpapi/generated"
)

// liveDocumentationAdminApplication delegates the administrative verbs to the
// real document practice service over the real repositories.
type liveDocumentationAdminApplication struct {
	service *appdocument.Service
}

func (a *liveDocumentationAdminApplication) StartDocumentationPractice(context.Context, string) (documentdomain.Workflow, error) {
	return documentdomain.Workflow{}, nil
}

func (a *liveDocumentationAdminApplication) ForceFailDocumentationWorkflow(ctx context.Context, workflowID, reason string, action *audit.HumanAction) (documentdomain.Workflow, error) {
	return a.service.ForceFail(ctx, workflowID, reason, action)
}

func (a *liveDocumentationAdminApplication) RestartDocumentationWorkflow(ctx context.Context, workflowID, reason string, action *audit.HumanAction) (documentdomain.Workflow, error) {
	return a.service.Restart(ctx, workflowID, reason, action)
}

func TestDeriveDocumentationStuckCoversEveryAttribution(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	longAgo := now.Add(-2 * time.Hour)
	agentStuckAfter := 15 * time.Minute

	// Agent phases report dwell timeouts; fresh states stay healthy.
	observation := postgres.DocumentWorkflowObservation{Workflow: documentdomain.Workflow{State: documentdomain.PlanReviewing, UpdatedAt: longAgo}}
	if stuck := deriveDocumentationStuck(observation, now, agentStuckAfter); !stuck.Flag || *stuck.Reason != "dwell_timeout" {
		t.Fatalf("stale agent phase = %#v", stuck)
	}
	fresh := postgres.DocumentWorkflowObservation{Workflow: documentdomain.Workflow{State: documentdomain.Generating, UpdatedAt: now.Add(-time.Minute)}}
	if stuck := deriveDocumentationStuck(fresh, now, agentStuckAfter); stuck.Flag {
		t.Fatalf("fresh agent phase = %#v", stuck)
	}

	// A terminal workflow is never stuck.
	failed := postgres.DocumentWorkflowObservation{Workflow: documentdomain.Workflow{State: documentdomain.Failed, UpdatedAt: longAgo}}
	if stuck := deriveDocumentationStuck(failed, now, agentStuckAfter); stuck.Flag {
		t.Fatalf("terminal workflow = %#v", stuck)
	}

	// Runnable phases attribute a dead action before falling back to dwell.
	failedAction := &postgres.DocumentBoundActionStatus{Phase: "verify", State: "failed", Attempt: 5, FailureClass: "infrastructure", FailureCode: "env-lost", FailureSummary: "environment vanished"}
	verifying := postgres.DocumentWorkflowObservation{Workflow: documentdomain.Workflow{State: documentdomain.Verifying, UpdatedAt: longAgo}, Action: failedAction}
	stuck := deriveDocumentationStuck(verifying, now, agentStuckAfter)
	if !stuck.Flag || *stuck.Reason != "action_failed" || *stuck.FailureClass != "infrastructure" || *stuck.FailureCode != "env-lost" || *stuck.FailureSummary != "environment vanished" {
		t.Fatalf("failed action attribution = %#v", stuck)
	}

	// Attempts exhausted while the action still claims to be running.
	runningAction := &postgres.DocumentBoundActionStatus{Phase: "materialize-artifact", State: "running", Attempt: 5}
	materializing := postgres.DocumentWorkflowObservation{Workflow: documentdomain.Workflow{State: documentdomain.MaterializingArtifact, UpdatedAt: longAgo}, Action: runningAction}
	if stuck = deriveDocumentationStuck(materializing, now, agentStuckAfter); !stuck.Flag || *stuck.Reason != "attempts_exhausted" {
		t.Fatalf("attempts exhausted attribution = %#v", stuck)
	}

	// Without a binding signal, the runnable dwell budget (1800s+300s) applies.
	quiet := postgres.DocumentWorkflowObservation{Workflow: documentdomain.Workflow{State: documentdomain.VerificationReviewing, UpdatedAt: now.Add(-2100 * time.Second - time.Second)}}
	if stuck = deriveDocumentationStuck(quiet, now, agentStuckAfter); !stuck.Flag || *stuck.Reason != "dwell_timeout" {
		t.Fatalf("runnable dwell attribution = %#v", stuck)
	}
	withinBudget := postgres.DocumentWorkflowObservation{Workflow: documentdomain.Workflow{State: documentdomain.VerificationReviewing, UpdatedAt: now.Add(-2000 * time.Second)}}
	if stuck = deriveDocumentationStuck(withinBudget, now, agentStuckAfter); stuck.Flag {
		t.Fatalf("runnable phase inside budget = %#v", stuck)
	}
}

func TestAdminDocumentationWorkflowObservationForceFailAndRestart(t *testing.T) {
	var application *liveDocumentationAdminApplication
	server := newAuthTestServer(t, func(cfg *config.Config, dependencies *Dependencies, database *postgres.Store) *kubernetes.Client {
		service, err := appdocument.NewService(database.DocumentPractice, database.Runnable)
		if err != nil {
			t.Fatalf("create document practice service: %v", err)
		}
		application = &liveDocumentationAdminApplication{service: service}
		dependencies.Documentation = application
		return nil
	})
	adminRegister := server.register(t, "alice", "alice-password")
	adminToken := server.login(t, "alice", "alice-password", adminRegister.TotpSecret)
	userRegister := server.register(t, "bob", "bob-password")
	userToken := server.login(t, "bob", "bob-password", userRegister.TotpSecret)

	// Seed a workflow stuck in an Agent stage for far beyond the threshold.
	stuckAt := time.Now().UTC().Add(-2 * time.Hour)
	stuck, err := documentdomain.NewWorkflow("document-workflow-observe-me", stuckAt)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.db.DocumentPractice.CreateWorkflow(context.Background(), stuck, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := server.db.DocumentPractice.AdvanceWorkflow(context.Background(), stuck.ID, stuck.StateVersion, documentdomain.PlanReviewing, stuckAt); err != nil {
		t.Fatal(err)
	}

	recorder := server.do(t, http.MethodGet, "/api/admin/documentation/workflows", userToken, nil)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("non-admin workflow list = %d, want 403", recorder.Code)
	}
	recorder = server.do(t, http.MethodGet, "/api/admin/documentation/workflows", adminToken, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("workflow list = %d: %s", recorder.Code, recorder.Body.String())
	}
	var list api.AdminDocumentationWorkflowList
	if err := json.Unmarshal(recorder.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Workflows) != 1 || list.Workflows[0].Id != "document-workflow-observe-me" {
		t.Fatalf("workflow list = %#v", list.Workflows)
	}
	summary := list.Workflows[0]
	if summary.State != "PlanReviewing" || summary.DwellSeconds < 7000 || !summary.Stuck.Flag || summary.Stuck.Reason == nil || *summary.Stuck.Reason != "dwell_timeout" {
		t.Fatalf("stuck workflow summary = %#v", summary)
	}

	recorder = server.do(t, http.MethodGet, "/api/admin/documentation/workflows/document-workflow-observe-me", adminToken, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("workflow detail = %d: %s", recorder.Code, recorder.Body.String())
	}
	var detail api.AdminDocumentationWorkflowDetail
	if err := json.Unmarshal(recorder.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if len(detail.Ledger) != 0 || len(detail.AgentAudits) != 0 || detail.Publication != nil {
		t.Fatalf("fresh workflow detail = %#v", detail)
	}

	// The reason is mandatory and bounded.
	recorder = server.do(t, http.MethodPost, "/api/admin/documentation/workflows/document-workflow-observe-me/force-fail", adminToken, api.AdminWorkflowReasonRequest{Reason: ""})
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("force fail without reason = %d, want 400", recorder.Code)
	}
	recorder = server.do(t, http.MethodPost, "/api/admin/documentation/workflows/document-workflow-observe-me/force-fail", adminToken, api.AdminWorkflowReasonRequest{Reason: strings.Repeat("r", 501)})
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("force fail with oversized reason = %d, want 400", recorder.Code)
	}
	recorder = server.do(t, http.MethodPost, "/api/admin/documentation/workflows/document-workflow-missing/force-fail", adminToken, api.AdminWorkflowReasonRequest{Reason: "any"})
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("force fail missing workflow = %d, want 404", recorder.Code)
	}
	recorder = server.do(t, http.MethodPost, "/api/admin/documentation/workflows/document-workflow-observe-me/force-fail", userToken, api.AdminWorkflowReasonRequest{Reason: "any"})
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("non-admin force fail = %d, want 403", recorder.Code)
	}

	recorder = server.do(t, http.MethodPost, "/api/admin/documentation/workflows/document-workflow-observe-me/force-fail", adminToken, api.AdminWorkflowReasonRequest{Reason: "agent stage crashed"})
	if recorder.Code != http.StatusOK {
		t.Fatalf("force fail = %d: %s", recorder.Code, recorder.Body.String())
	}
	var forced api.AdminDocumentationWorkflow
	if err := json.Unmarshal(recorder.Body.Bytes(), &forced); err != nil {
		t.Fatal(err)
	}
	if forced.State != "Failed" || forced.StateVersion != int(stuck.StateVersion+2) {
		t.Fatalf("forced summary = %#v", forced)
	}
	// Repeating the verb conflicts with the terminal state.
	recorder = server.do(t, http.MethodPost, "/api/admin/documentation/workflows/document-workflow-observe-me/force-fail", adminToken, api.AdminWorkflowReasonRequest{Reason: "again"})
	if recorder.Code != http.StatusConflict {
		t.Fatalf("repeat force fail = %d, want 409", recorder.Code)
	}
	// The twin record: one ledger entry plus one human audit row with the
	// transition summary.
	artifacts, err := server.db.DocumentPractice.ListArtifacts(context.Background(), stuck.ID)
	if err != nil || len(artifacts) != 1 || artifacts[0].Kind != "admin.force_fail" || artifacts[0].OwnerRole != "admin" {
		t.Fatalf("force fail ledger = %#v, %v", artifacts, err)
	}
	auditRows, err := server.db.Audit.ListHumanActions(context.Background(), postgres.HumanActionFilter{Action: audit.ActionDocumentationWorkflowForceFail, Limit: 5})
	if err != nil || len(auditRows) != 1 {
		t.Fatalf("force fail audits = %#v, %v", auditRows, err)
	}
	var forceDetail map[string]string
	if err := json.Unmarshal(auditRows[0].Detail, &forceDetail); err != nil {
		t.Fatal(err)
	}
	if forceDetail["reason"] != "agent stage crashed" || forceDetail["from_state"] != "PlanReviewing" || forceDetail["to_state"] != "Failed" {
		t.Fatalf("force fail audit detail = %#v", forceDetail)
	}

	// Restart resets the failed workflow to Planning and advances the revision.
	recorder = server.do(t, http.MethodPost, "/api/admin/documentation/workflows/document-workflow-observe-me/restart", adminToken, api.AdminWorkflowReasonRequest{Reason: "retry after fix"})
	if recorder.Code != http.StatusOK {
		t.Fatalf("restart = %d: %s", recorder.Code, recorder.Body.String())
	}
	var restarted api.AdminDocumentationWorkflow
	if err := json.Unmarshal(recorder.Body.Bytes(), &restarted); err != nil {
		t.Fatal(err)
	}
	if restarted.State != "Planning" || restarted.Revision != forced.Revision+1 || restarted.Stuck.Flag {
		t.Fatalf("restarted summary = %#v", restarted)
	}
	recorder = server.do(t, http.MethodPost, "/api/admin/documentation/workflows/document-workflow-observe-me/restart", adminToken, api.AdminWorkflowReasonRequest{Reason: "again"})
	if recorder.Code != http.StatusConflict {
		t.Fatalf("restart from Planning = %d, want 409", recorder.Code)
	}
	artifacts, err = server.db.DocumentPractice.ListArtifacts(context.Background(), stuck.ID)
	if err != nil || len(artifacts) != 2 || artifacts[1].Kind != "admin.restart" {
		t.Fatalf("restart ledger = %#v, %v", artifacts, err)
	}
	auditRows, err = server.db.Audit.ListHumanActions(context.Background(), postgres.HumanActionFilter{Action: audit.ActionDocumentationWorkflowRestart, Limit: 5})
	if err != nil || len(auditRows) != 1 {
		t.Fatalf("restart audits = %#v, %v", auditRows, err)
	}
}

func TestAdminRunnableActionsEndpointAndMetrics(t *testing.T) {
	server := newAuthTestServerSimple(t, nil)
	adminRegister := server.register(t, "alice", "alice-password")
	adminToken := server.login(t, "alice", "alice-password", adminRegister.TotpSecret)
	userRegister := server.register(t, "bob", "bob-password")
	userToken := server.login(t, "bob", "bob-password", userRegister.TotpSecret)

	recorder := server.do(t, http.MethodGet, "/api/admin/runnable-actions", userToken, nil)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("non-admin queue = %d, want 403", recorder.Code)
	}
	recorder = server.do(t, http.MethodGet, "/api/admin/runnable-actions?state=bogus", adminToken, nil)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("bogus state filter = %d, want 400", recorder.Code)
	}

	// An empty queue still answers with an explicit summary.
	recorder = server.do(t, http.MethodGet, "/api/admin/runnable-actions", adminToken, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("queue = %d: %s", recorder.Code, recorder.Body.String())
	}
	var page api.AdminRunnableActionPage
	if err := json.Unmarshal(recorder.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 0 || page.Summary.ByState == nil {
		t.Fatalf("empty queue page = %#v", page)
	}

	// The metrics document carries the two new gauges with full state series.
	recorder = server.do(t, http.MethodGet, "/metrics", "", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("metrics = %d", recorder.Code)
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		"# TYPE breakfix_document_workflows gauge",
		"# TYPE breakfix_runnable_actions gauge",
		`breakfix_document_workflows{state="Planning"} 0`,
		`breakfix_document_workflows{state="Failed"} 0`,
		`breakfix_runnable_actions{state="queued"} 0`,
		`breakfix_runnable_actions{state="failed"} 0`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("metrics missing %q in:\n%s", expected, body)
		}
	}
}
