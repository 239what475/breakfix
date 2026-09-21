package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/adapter/postgres"
	"github.com/breakfix/breakfix/internal/domain/runnable"
	testpostgres "github.com/breakfix/breakfix/internal/testkit/postgres"
	api "github.com/breakfix/breakfix/internal/transport/httpapi/generated"
	"github.com/gin-gonic/gin"
)

func adminReapTestPlan(t *testing.T) runnable.BlankRuntimePlan {
	t.Helper()
	image := "registry.example/breakfix/terminal@sha256:" + strings.Repeat("d", 64)
	plan := runnable.BlankRuntimePlan{
		Profile: runnable.RuntimeProfile{
			Runtime: runnable.RuntimeK8s, ProfileRevision: "k8s-profile-1",
			BaseImage:        image,
			SoftwareVersions: map[string]string{"kubernetes": "v1.30.0"},
			Resources:        runnable.ResourceLimits{CPU: "1", MemoryBytes: 1 << 30, EphemeralBytes: 1 << 30, MaxProcesses: 64, MaxConcurrentTasks: 1},
			Network:          runnable.NetworkPrivate, Topology: "single-kubernetes-cluster",
			ExecutionBoundaries: []runnable.ExecutionBoundary{
				{ID: "management-write", Target: runnable.TargetLocation{Kind: "management", ID: "cluster"}, Permission: runnable.PermissionReadWrite, Network: runnable.NetworkPrivate, MaxTimeout: 900},
				{ID: "management-read", Target: runnable.TargetLocation{Kind: "management", ID: "cluster"}, Permission: runnable.PermissionReadOnly, Network: runnable.NetworkPrivate, MaxTimeout: 900},
			},
		},
		Lifecycle: runnable.LifecyclePolicy{CreateTimeoutSeconds: 60, ResetTimeoutSeconds: 60, StopTimeoutSeconds: 60, ReapTimeoutSeconds: 60, IdleTTLSeconds: 600, MaxLifetimeSeconds: 1800},
		Image:     image,
	}
	if err := plan.Validate(); err != nil {
		t.Fatalf("blank plan fixture: %v", err)
	}
	return plan
}

func enqueueAdminTestReap(t *testing.T, database *postgres.Store, plan runnable.BlankRuntimePlan, digest, name string) {
	t.Helper()
	digestValue, err := plan.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if digestValue != digest {
		t.Fatal("test digest fence mismatch")
	}
	request := runnable.ReapRequest{
		Namespace: "breakfix-system", Name: name, UID: name + "-uid", Revision: digest,
		Binding: runnable.EnvironmentBinding{Namespace: "breakfix-system", Name: name, UID: name + "-uid", Purpose: runnable.PurposeLearning, BlankRuntime: &plan},
	}
	if err := database.Runnable.Enqueue(context.Background(), request); err != nil {
		t.Fatalf("enqueue reap %s: %v", name, err)
	}
}

func listAdminRunnableReaps(t *testing.T, handler *Handler) (int, api.AdminRunnableReapList) {
	t.Helper()
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/admin/runnable-reaps", nil)
	handler.ListAdminRunnableReaps(ctx)
	if recorder.Code != http.StatusOK {
		t.Fatalf("list reaps = %d %s", recorder.Code, recorder.Body.String())
	}
	var page api.AdminRunnableReapList
	if err := json.NewDecoder(recorder.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	return recorder.Code, page
}

func TestListAdminRunnableReapsReportsQueueState(t *testing.T) {
	database := testpostgres.New(t)
	handler := &Handler{db: database}
	plan := adminReapTestPlan(t)
	digest, err := plan.Digest()
	if err != nil {
		t.Fatal(err)
	}

	enqueueAdminTestReap(t, database, plan, digest, "environment-first")
	enqueueAdminTestReap(t, database, plan, digest, "environment-second")
	// One claimed-and-failed reap carries the diagnostic operators need to see;
	// the claimer picks the row with the earliest schedule, whichever it is.
	claim, err := database.Runnable.Claim(context.Background(), "reaper-a", time.Minute, time.Now().UTC().Add(time.Second))
	if err != nil || claim == nil {
		t.Fatalf("claim reap = %#v %v", claim, err)
	}
	if err := database.Runnable.Complete(context.Background(), *claim, false, "namespace ownership metadata mismatch", time.Now().UTC(), time.Now().UTC().Add(5*time.Second)); err != nil {
		t.Fatalf("retry reap: %v", err)
	}
	retriedKey := claim.Record.Request.Key()

	_, page := listAdminRunnableReaps(t, handler)
	if len(page.Reaps) != 2 {
		t.Fatalf("reaps = %#v", page.Reaps)
	}
	byKey := map[string]api.AdminRunnableReap{}
	for _, reap := range page.Reaps {
		byKey[reap.ReapKey] = reap
	}
	retried := byKey[retriedKey]
	if retried.ReapKey == "" || retried.State != api.AdminRunnableReapStateQueued || retried.Attempt != 1 || retried.LastError != "namespace ownership metadata mismatch" || retried.NextAttemptAt.IsZero() || retried.UpdatedAt.IsZero() {
		t.Fatalf("retried reap = %#v", retried)
	}
	for _, reap := range page.Reaps {
		if reap.ReapKey == retriedKey {
			continue
		}
		if reap.State != api.AdminRunnableReapStateQueued || reap.Attempt != 0 || reap.LastError != "" || reap.NextAttemptAt.IsZero() || reap.UpdatedAt.IsZero() {
			t.Fatalf("untouched reap = %#v", reap)
		}
	}
}

func TestListAdminRunnableReapsCapsTheObservation(t *testing.T) {
	database := testpostgres.New(t)
	handler := &Handler{db: database}
	plan := adminReapTestPlan(t)
	digest, err := plan.Digest()
	if err != nil {
		t.Fatal(err)
	}
	for index := range adminRunnableReapsLimit + 1 {
		enqueueAdminTestReap(t, database, plan, digest, fmt.Sprintf("environment-%03d", index))
	}
	_, page := listAdminRunnableReaps(t, handler)
	if len(page.Reaps) != adminRunnableReapsLimit {
		t.Fatalf("capped reaps = %d, want %d", len(page.Reaps), adminRunnableReapsLimit)
	}
}

func TestListAdminRunnableReapsRequiresTheQueue(t *testing.T) {
	handler := &Handler{}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/admin/runnable-reaps", nil)
	handler.ListAdminRunnableReaps(ctx)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("list reaps without a store = %d %s", recorder.Code, recorder.Body.String())
	}
}
