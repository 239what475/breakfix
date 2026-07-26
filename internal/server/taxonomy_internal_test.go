package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/agentruntime"
	"github.com/breakfix/breakfix/internal/config"
	"github.com/breakfix/breakfix/internal/taxonomy"
	"github.com/breakfix/breakfix/internal/testpostgres"
	"github.com/gin-gonic/gin"
)

func TestInternalTaxonomyContextRejectsStaleLease(t *testing.T) {
	database := testpostgres.New(t)
	ctx := context.Background()
	item, err := database.EnqueueTaxonomyWork(ctx, taxonomy.WorkItem{
		ID: "taxonomy-work", Kind: taxonomy.WorkKindMapping, ChallengeID: "challenge-one", ChallengeRevision: "sha256:test", State: taxonomy.WorkPending,
	})
	if err != nil {
		t.Fatal(err)
	}
	claimedWork, err := database.ClaimTaxonomyWork(ctx, "server", time.Minute)
	if err != nil || claimedWork == nil {
		t.Fatalf("claim taxonomy work = %#v, %v", claimedWork, err)
	}
	input, err := json.Marshal(taxonomy.RunInput{WorkID: item.ID, Stage: taxonomy.WorkStageMapper, Round: 0})
	if err != nil {
		t.Fatal(err)
	}
	run, err := database.ScheduleTaxonomyRun(ctx, *claimedWork, agentruntime.CreateRun{
		ID: "taxonomy-run", Purpose: taxonomy.RuntimePurposeMapper, OwnerKind: "taxonomy-work", OwnerRef: item.ID,
		Input: input, Model: "test-model", PromptVersion: "test", DeadlineAt: time.Now().UTC().Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := database.ClaimNext(ctx, "worker-one", time.Minute, time.Now().UTC())
	if err != nil || first == nil {
		t.Fatalf("claim first taxonomy attempt = %#v, %v", first, err)
	}
	now := time.Now().UTC()
	if err := database.Requeue(ctx, *first, now, "worker lost", now); err != nil {
		t.Fatal(err)
	}
	second, err := database.ClaimNext(ctx, "worker-two", time.Minute, now.Add(time.Millisecond))
	if err != nil || second == nil || second.Run.Attempt != 2 {
		t.Fatalf("claim replacement taxonomy attempt = %#v, %v", second, err)
	}

	handler := NewHandler(database, nil, config.Config{
		DataDir:        t.TempDir(),
		InternalAPIKey: "internal-test-key",
		Agent:          config.AgentConfig{Model: "test-model"},
	})
	router := gin.New()
	router.POST("/api/internal/agent-runs/:id/taxonomy/context", handler.InternalTaxonomyContext)
	body, err := json.Marshal(taxonomy.LeaseCredential{Attempt: first.Run.Attempt, LeaseOwner: first.LeaseOwner})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/internal/agent-runs/"+run.ID+"/taxonomy/context", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Breakfix-Internal-Key", "internal-test-key")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("stale taxonomy context status = %d, want %d: %s", response.Code, http.StatusConflict, response.Body.String())
	}
}
