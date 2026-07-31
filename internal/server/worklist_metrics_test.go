package server

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/config"
	"github.com/breakfix/breakfix/internal/testpostgres"
	"github.com/breakfix/breakfix/internal/worklist"
	"github.com/gin-gonic/gin"
)

func TestWorklistInspectionAndMetricsAreServerOwned(t *testing.T) {
	gin.SetMode(gin.TestMode)
	database := testpostgres.New(t)
	now := time.Now().UTC()
	if _, err := database.EnqueueWorkItem(context.Background(), worklist.CreateItem{
		ID: "work-observable-cleanup", Kind: worklist.KindArtifactCleanup,
		SubjectType: worklist.SubjectCandidateRevision, SubjectID: "candidate-observable", NextRunAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(database, nil, config.Config{DataDir: t.TempDir(), InternalWorkers: testInternalWorkerKeys()})
	handler.worklistMetrics.recordFailure(worklist.KindVerify, "renew")

	inspection := httptest.NewRecorder()
	inspectionContext, _ := gin.CreateTestContext(inspection)
	inspectionContext.Request = httptest.NewRequest(http.MethodPost, "/api/internal/work-items/inspect",
		bytes.NewBufferString(`{"kind":"artifact_cleanup","state":"pending","limit":10}`))
	inspectionContext.Request.Header.Set("X-Breakfix-Internal-Key", "agent-test-key")
	handler.InternalInspectWorkItems(inspectionContext)
	if inspection.Code != http.StatusOK || !strings.Contains(inspection.Body.String(), "work-observable-cleanup") {
		t.Fatalf("inspection response = %d %s", inspection.Code, inspection.Body.String())
	}

	metrics := httptest.NewRecorder()
	metricsContext, _ := gin.CreateTestContext(metrics)
	metricsContext.Request = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	handler.WorklistMetrics(metricsContext)
	if metrics.Code != http.StatusOK {
		t.Fatalf("metrics response = %d %s", metrics.Code, metrics.Body.String())
	}
	for _, expected := range []string{
		`breakfix_work_items{kind="artifact_cleanup",state="pending"} 1`,
		`breakfix_cleanup_backlog 1`,
		`breakfix_verification_environments 0`,
		`breakfix_work_item_operation_failures_total{kind="verify",operation="renew"} 1`,
	} {
		if !strings.Contains(metrics.Body.String(), expected) {
			t.Fatalf("metrics do not contain %q:\n%s", expected, metrics.Body.String())
		}
	}
}
