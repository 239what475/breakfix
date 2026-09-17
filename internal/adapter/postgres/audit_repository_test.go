package postgres

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/domain/audit"
)

func seedHumanActions(t *testing.T, database *Store, actions ...audit.HumanAction) {
	t.Helper()
	for _, action := range actions {
		if err := database.Audit.RecordHumanAction(context.Background(), action); err != nil {
			t.Fatalf("record human action %q: %v", action.ID, err)
		}
	}
}

func TestRecordHumanActionValidatesTheClosedVocabulary(t *testing.T) {
	database := newTestDB(t)
	now := time.Now().UTC()
	valid := audit.HumanAction{ID: audit.NewID(now), UserID: "u-1", Action: audit.ActionEnvironmentRelease, TargetType: audit.TargetRuntimeEnvironment, TargetID: "env-1", Detail: json.RawMessage(`{"reason":"cleanup"}`), CreatedAt: now}
	if err := database.Audit.RecordHumanAction(context.Background(), valid); err != nil {
		t.Fatalf("record valid action: %v", err)
	}
	unregistered := valid
	unregistered.ID = audit.NewID(now.Add(time.Second))
	unregistered.Action = "workflow.cancel"
	if err := database.Audit.RecordHumanAction(context.Background(), unregistered); err == nil {
		t.Fatalf("unregistered action vocabulary was accepted")
	}
	rows, err := database.Audit.ListHumanActions(context.Background(), HumanActionFilter{Limit: 10})
	if err != nil {
		t.Fatalf("list human actions: %v", err)
	}
	if len(rows) != 1 || rows[0].Action != audit.ActionEnvironmentRelease {
		t.Fatalf("audit rows = %#v, want only the registered action", rows)
	}
}

func TestListHumanActionsFiltersAndPagesByKeyset(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	base := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)
	rows := []audit.HumanAction{
		{ID: "audit-1", UserID: "u-admin", Action: audit.ActionDocumentationPracticeStart, TargetType: audit.TargetDocumentWorkflow, TargetID: "wf-1", Detail: json.RawMessage(`{}`), CreatedAt: base},
		{ID: "audit-2", UserID: "u-admin", Action: audit.ActionUserTOTPReset, TargetType: audit.TargetUser, TargetID: "u-victim", Detail: json.RawMessage(`{}`), CreatedAt: base.Add(time.Minute)},
		{ID: "audit-3", UserID: "u-other", Action: audit.ActionDocumentationPracticeStart, TargetType: audit.TargetDocumentWorkflow, TargetID: "wf-2", Detail: json.RawMessage(`{}`), CreatedAt: base.Add(2 * time.Minute)},
		{ID: "audit-4", UserID: "u-admin", Action: audit.ActionDocumentationWorkflowForceFail, TargetType: audit.TargetDocumentWorkflow, TargetID: "wf-1", Detail: json.RawMessage(`{"reason":"stuck"}`), CreatedAt: base.Add(3 * time.Minute)},
	}
	seedHumanActions(t, database, rows...)

	byAction, err := database.Audit.ListHumanActions(ctx, HumanActionFilter{Action: audit.ActionDocumentationPracticeStart, Limit: 10})
	if err != nil {
		t.Fatalf("list by action: %v", err)
	}
	if len(byAction) != 2 || byAction[0].ID != "audit-3" || byAction[1].ID != "audit-1" {
		t.Fatalf("action filter rows = %#v, want newest first", byAction)
	}

	byUser, err := database.Audit.ListHumanActions(ctx, HumanActionFilter{UserID: "u-admin", Limit: 10})
	if err != nil {
		t.Fatalf("list by user: %v", err)
	}
	if len(byUser) != 3 || byUser[0].ID != "audit-4" {
		t.Fatalf("user filter rows = %#v", byUser)
	}

	combined, err := database.Audit.ListHumanActions(ctx, HumanActionFilter{Action: audit.ActionDocumentationPracticeStart, UserID: "u-other", Limit: 10})
	if err != nil {
		t.Fatalf("list combined: %v", err)
	}
	if len(combined) != 1 || combined[0].ID != "audit-3" {
		t.Fatalf("combined filter rows = %#v", combined)
	}

	first, err := database.Audit.ListHumanActions(ctx, HumanActionFilter{Limit: 2})
	if err != nil || len(first) != 2 || first[0].ID != "audit-4" || first[1].ID != "audit-3" {
		t.Fatalf("first page = %#v, %v", first, err)
	}
	second, err := database.Audit.ListHumanActions(ctx, HumanActionFilter{Limit: 2, Cursor: &audit.HumanActionCursor{CreatedAt: first[1].CreatedAt, ID: first[1].ID}})
	if err != nil || len(second) != 2 || second[0].ID != "audit-2" || second[1].ID != "audit-1" {
		t.Fatalf("second page = %#v, %v", second, err)
	}
	empty, err := database.Audit.ListHumanActions(ctx, HumanActionFilter{Limit: 2, Cursor: &audit.HumanActionCursor{CreatedAt: second[1].CreatedAt, ID: second[1].ID}})
	if err != nil || len(empty) != 0 {
		t.Fatalf("page past the end = %#v, %v", empty, err)
	}

	if _, err := database.Audit.ListHumanActions(ctx, HumanActionFilter{Limit: 0}); err == nil {
		t.Fatalf("zero limit was accepted")
	}
	if _, err := database.Audit.ListHumanActions(ctx, HumanActionFilter{Limit: 2, Cursor: &audit.HumanActionCursor{ID: "audit-1"}}); err == nil {
		t.Fatalf("cursor without timestamp was accepted")
	}
}
