package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/api"
	"github.com/breakfix/breakfix/internal/challenge"
	"github.com/breakfix/breakfix/internal/config"
	"github.com/breakfix/breakfix/internal/k8s"
	breakfixv1 "github.com/breakfix/breakfix/internal/k8s/apis/breakfix/v1"
	"github.com/breakfix/breakfix/internal/testpostgres"
	"github.com/gin-gonic/gin"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestGetChallengeProgressRejectsRequestsWithoutAnEnvironment(t *testing.T) {
	handler := newProgressTestHandler(t, nil)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/challenges/demo/progress", nil)
	ctx.Set("user_id", "u-demo")

	handler.GetChallengeProgress(ctx, "demo")

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("expected 404 without environment, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestAssistantWindowsIncludeOnlyValidatedWorkspaceTabs(t *testing.T) {
	windows, current, err := assistantWindows("shell-2", []string{"shell-1", "shell-2", "shell-1"})
	if err != nil {
		t.Fatal(err)
	}
	if current != "shell-2" || len(windows) != 2 || windows[0] != "shell-1" || windows[1] != "shell-2" {
		t.Fatalf("assistant windows = %#v, current = %q", windows, current)
	}
	if _, _, err := assistantWindows("invalid window", nil); err == nil {
		t.Fatal("invalid terminal window accepted")
	}
}

func TestGetChallengeProgressRejectsNonReadyEnvironment(t *testing.T) {
	handler := newProgressTestHandler(t, []breakfixv1.ContainerEnvironment{{
		Spec:   breakfixv1.CommonEnvironmentSpec{ChallengeRef: "demo", UserRef: "u-demo"},
		Status: breakfixv1.CommonEnvironmentStatus{Phase: breakfixv1.EnvironmentProvisioning},
	}})
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/challenges/demo/progress", nil)
	ctx.Set("user_id", "u-demo")

	handler.GetChallengeProgress(ctx, "demo")

	if recorder.Code != http.StatusConflict {
		t.Fatalf("expected 409 for non-ready environment, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestGetChallengeProgressSurfacesCheckpointRunnerFailures(t *testing.T) {
	handler := newProgressTestHandler(t, []breakfixv1.ContainerEnvironment{{
		Spec: breakfixv1.CommonEnvironmentSpec{ChallengeRef: "demo", UserRef: "u-demo"},
		Status: breakfixv1.CommonEnvironmentStatus{
			Phase: breakfixv1.EnvironmentReady,
			Checkpoints: &breakfixv1.CheckpointStatus{
				Error: "checkpoint runner exited with 1",
			},
		},
	}})
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/challenges/demo/progress", nil)
	ctx.Set("user_id", "u-demo")

	handler.GetChallengeProgress(ctx, "demo")

	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for failed checkpoint runner, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestGetChallengeProgressReturnsControllerCheckpointSnapshot(t *testing.T) {
	firstPassedAt := metav1.NewTime(time.Date(2026, time.July, 28, 6, 0, 0, 0, time.UTC))
	handler := newProgressTestHandler(t, []breakfixv1.ContainerEnvironment{{
		Spec: breakfixv1.CommonEnvironmentSpec{ChallengeRef: "demo", UserRef: "u-demo"},
		Status: breakfixv1.CommonEnvironmentStatus{
			Phase: breakfixv1.EnvironmentCompleted,
			Checkpoints: &breakfixv1.CheckpointStatus{Results: []breakfixv1.CheckpointResultStatus{{
				ID: "complete", Summary: "done", Passed: true, FirstPassedAt: &firstPassedAt,
			}}},
		},
	}})
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/challenges/demo/progress", nil)
	ctx.Set("user_id", "u-demo")

	handler.GetChallengeProgress(ctx, "demo")

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var progress api.ChallengeProgress
	if err := json.Unmarshal(recorder.Body.Bytes(), &progress); err != nil {
		t.Fatal(err)
	}
	if len(progress.Checks) != 1 || !progress.Checks[0].Passed || progress.Checks[0].FirstPassedAt == nil || !progress.Checks[0].FirstPassedAt.Equal(firstPassedAt.Time) {
		t.Fatalf("unexpected checkpoint snapshot: %#v", progress.Checks)
	}
}

func TestGetChallengeProgressRequiresLogin(t *testing.T) {
	handler := NewHandler(nil, nil, config.Config{})
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/challenges/demo/progress", nil)

	handler.GetChallengeProgress(ctx, "demo")

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without login, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestGetChallengeContentReturnsPublishedAssetsForAuthenticatedUser(t *testing.T) {
	gin.SetMode(gin.TestMode)

	root := t.TempDir()
	challengesDir := filepath.Join(root, "challenges")
	challengeDir := filepath.Join(challengesDir, "demo")
	writeTestFile(t, filepath.Join(challengeDir, "challenge.yaml"), "id: demo\nsource_slug: demo\ntitle: Demo\ntype: script\nruntime: container\ndifficulty: easy\ndescription: demo\nimage: demo:v1\npublished_at: 2026-07-24T09:00:00Z\ncheckpoints:\n  - id: complete\n    title: Complete\n    description: Complete the task\n    hint: hints/complete.md\n")
	writeTestFile(t, filepath.Join(challengeDir, "Dockerfile"), "FROM breakfix-base:latest\n")
	writeTestFile(t, filepath.Join(challengeDir, "generate.sh"), "#!/bin/sh\n")
	writeTestFile(t, filepath.Join(challengeDir, "problem.md"), "# Problem\nRepair it.\n")
	writeTestFile(t, filepath.Join(challengeDir, "solution.md"), "# Solution\n<!-- checkpoint: complete -->\nRepair it this way.\n")
	writeTestFile(t, filepath.Join(challengeDir, "hints", "complete.md"), "Look at the service.\n")
	writeTestFile(t, filepath.Join(challengeDir, "checks", "checkpoints.sh"), "#!/bin/sh\n")
	writeTestFile(t, filepath.Join(challengeDir, "answer.sh"), "#!/bin/sh\nexit 0\n")
	seedTestTaxonomy(t, root)

	database := testpostgres.New(t)
	if _, err := database.CreateUserWithAuth("u-demo", "demo", "hash", "totp"); err != nil {
		t.Fatal(err)
	}

	handler := NewHandler(database, nil, config.Config{DataDir: root})
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/challenges/demo/content", nil)
	ctx.Set("user_id", "u-demo")

	handler.GetChallengeContent(ctx, "demo")

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var content api.ChallengeContent
	if err := json.Unmarshal(recorder.Body.Bytes(), &content); err != nil {
		t.Fatal(err)
	}
	if content.Problem != "# Problem\nRepair it.\n" {
		t.Fatalf("unexpected problem: %#v", content.Problem)
	}
	if content.Hints["complete"] != "Look at the service.\n" {
		t.Fatalf("unexpected hints: %#v", content.Hints)
	}
	if content.Taxonomy.PrimaryOutcome.Id != "skill-4444444444444444" || len(content.Taxonomy.EntrySkills) != 1 {
		t.Fatalf("taxonomy projection = %#v", content.Taxonomy)
	}
	entry := content.Taxonomy.EntrySkills[0]
	if entry.Id != "skill-5555555555555555" || len(entry.Requires) != 1 || entry.Requires[0].Id != "skill-6666666666666666" {
		t.Fatalf("entry skill prerequisites = %#v", entry)
	}
}

func TestListChallengesIncludesRuntime(t *testing.T) {
	gin.SetMode(gin.TestMode)

	root := t.TempDir()
	challengesDir := filepath.Join(root, "challenges")
	challengeDir := filepath.Join(challengesDir, "demo")
	if err := os.MkdirAll(challengeDir, 0755); err != nil {
		t.Fatal(err)
	}

	writeTestFile(t, filepath.Join(challengeDir, "challenge.yaml"), "id: demo\nsource_slug: demo\ntitle: Demo\ntype: script\nruntime: vcluster\ndifficulty: easy\ndescription: demo\nimage: demo:v1\npublished_at: 2026-07-24T09:00:00Z\ncheckpoints:\n  - id: complete\n    title: Complete\n    description: Complete the task\n    hint: hints/complete.md\n")
	writeTestFile(t, filepath.Join(challengeDir, "Dockerfile"), "FROM breakfix-k8s-base:latest\n")
	writeTestFile(t, filepath.Join(challengeDir, "generate.sh"), "#!/bin/sh\n")
	writeTestFile(t, filepath.Join(challengeDir, "problem.md"), "problem\n")
	writeTestFile(t, filepath.Join(challengeDir, "solution.md"), "<!-- checkpoint: complete -->\nsolution\n")
	writeTestFile(t, filepath.Join(challengeDir, "hints", "complete.md"), "hint\n")
	writeTestFile(t, filepath.Join(challengeDir, "checks", "checkpoints.sh"), "#!/bin/sh\n")
	writeTestFile(t, filepath.Join(challengeDir, "answer.sh"), "#!/bin/sh\nexit 0\n")
	seedTestTaxonomy(t, root)

	cfg := config.Config{DataDir: root}
	handler := NewHandler(nil, nil, cfg)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/challenges", nil)

	handler.ListChallenges(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp api.ChallengeList
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Challenges) != 1 {
		t.Fatalf("expected 1 challenge, got %#v", resp.Challenges)
	}
	got := resp.Challenges[0]
	if got.Runtime != api.ChallengeSummaryRuntimeVcluster {
		t.Fatalf("expected runtime vcluster, got %#v", got.Runtime)
	}
	if !got.PublishedAt.Equal(time.Date(2026, time.July, 24, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("unexpected published time: %#v", got.PublishedAt)
	}
	if got.Active != nil || got.Solved != nil || got.Progress != nil {
		t.Fatalf("guest catalog exposed personal state: %#v", got)
	}
	if len(got.Tags) != 1 || got.Tags[0].Id != "tag-4444444444444444" || got.Tags[0].Title != "Test" {
		t.Fatalf("structured tags = %#v", got.Tags)
	}
	if got.PrimaryOutcome.Id != "skill-4444444444444444" || got.PrimaryOutcome.Title != "Repair a test service" {
		t.Fatalf("primary outcome = %#v", got.PrimaryOutcome)
	}
}

func TestListChallengesHidesPublishedChallengeBeforeTaxonomyMapping(t *testing.T) {
	gin.SetMode(gin.TestMode)

	root := t.TempDir()
	writeTestChallenge(t, root)
	handler := NewHandler(nil, nil, config.Config{DataDir: root})

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/challenges", nil)
	handler.ListChallenges(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var response api.ChallengeList
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Challenges) != 0 {
		t.Fatalf("unmapped published challenge entered public catalog: %#v", response.Challenges)
	}
}

func TestListChallengesMergesCurrentProgressWithDurableCompletion(t *testing.T) {
	handler := newProgressTestHandler(t, []breakfixv1.ContainerEnvironment{{
		Spec: breakfixv1.CommonEnvironmentSpec{ChallengeRef: "demo", UserRef: "u-demo"},
		Status: breakfixv1.CommonEnvironmentStatus{
			Phase: breakfixv1.EnvironmentReady,
			Checkpoints: &breakfixv1.CheckpointStatus{Results: []breakfixv1.CheckpointResultStatus{{
				ID: "complete", Passed: true, Summary: "done",
			}}},
		},
	}})
	if err := handler.db.RecordChallengeCompletion(context.Background(), "u-demo", "demo", "previous-environment", time.Date(2026, time.July, 24, 10, 30, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/challenges", nil)
	c.Set("user_id", "u-demo")
	handler.ListChallenges(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var response api.ChallengeList
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Challenges) != 1 {
		t.Fatalf("unexpected challenges: %#v", response.Challenges)
	}
	got := response.Challenges[0]
	if got.Active == nil || !*got.Active || got.Solved == nil || !*got.Solved {
		t.Fatalf("unexpected user state: %#v", got)
	}
	if got.Progress == nil || got.Progress.Passed != 1 || got.Progress.Total != 1 {
		t.Fatalf("unexpected checkpoint progress: %#v", got.Progress)
	}
}

func TestListChallengesShowsCompletedEnvironmentBeforeSQLProjection(t *testing.T) {
	handler := newProgressTestHandler(t, []breakfixv1.ContainerEnvironment{{
		Spec: breakfixv1.CommonEnvironmentSpec{ChallengeRef: "demo", UserRef: "u-demo"},
		Status: breakfixv1.CommonEnvironmentStatus{
			Phase: breakfixv1.EnvironmentCompleted,
			Checkpoints: &breakfixv1.CheckpointStatus{Results: []breakfixv1.CheckpointResultStatus{{
				ID: "complete", Passed: true, Summary: "done",
			}}},
		},
	}})

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/challenges", nil)
	ctx.Set("user_id", "u-demo")
	handler.ListChallenges(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var response api.ChallengeList
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Challenges) != 1 {
		t.Fatalf("unexpected challenges: %#v", response.Challenges)
	}
	got := response.Challenges[0]
	if got.Solved == nil || !*got.Solved || got.Active == nil || *got.Active || got.Progress != nil {
		t.Fatalf("completed environment was not reflected immediately: %#v", got)
	}
}

func TestListChallengesKeepsCompletionAfterEnvironmentIsGone(t *testing.T) {
	handler := newProgressTestHandler(t, nil)
	if err := handler.db.RecordChallengeCompletion(context.Background(), "u-demo", "demo", "completed-environment", time.Date(2026, time.July, 24, 10, 45, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/challenges", nil)
	c.Set("user_id", "u-demo")
	handler.ListChallenges(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var response api.ChallengeList
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	got := response.Challenges[0]
	if got.Solved == nil || !*got.Solved || got.Active == nil || *got.Active || got.Progress != nil {
		t.Fatalf("unexpected durable completion state: %#v", got)
	}
}

func TestChallengeArtifactRevisionMismatchRemovesChallengeFromPublicEndpoints(t *testing.T) {
	handler := newProgressTestHandler(t, nil)
	if _, err := handler.publishedChallenge("demo"); err != nil {
		t.Fatalf("published challenge was unavailable before artifact change: %v", err)
	}
	if err := os.WriteFile(filepath.Join(handler.challengesDir, "demo", "problem.md"), []byte("changed problem\n"), 0644); err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/challenges", nil)
	handler.ListChallenges(ctx)
	if recorder.Code != http.StatusOK {
		t.Fatalf("catalog status = %d: %s", recorder.Code, recorder.Body.String())
	}
	var list api.ChallengeList
	if err := json.Unmarshal(recorder.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Challenges) != 0 {
		t.Fatalf("stale taxonomy mapping left challenge public: %#v", list.Challenges)
	}
	if _, err := handler.publishedChallenge("demo"); !errors.Is(err, challenge.ErrNotFound) {
		t.Fatalf("stale mapping challenge lookup error = %v, want not found", err)
	}
	for name, handlerFunc := range map[string]func(*gin.Context, string){
		"content": handler.GetChallengeContent,
		"start":   handler.StartChallenge,
	} {
		recorder := httptest.NewRecorder()
		requestContext, _ := gin.CreateTestContext(recorder)
		requestContext.Request = httptest.NewRequest(http.MethodGet, "/api/challenges/demo/"+name, nil)
		requestContext.Set("user_id", "u-demo")
		handlerFunc(requestContext, "demo")
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("%s stale mapping status = %d: %s", name, recorder.Code, recorder.Body.String())
		}
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func newProgressTestHandler(t *testing.T, environments []breakfixv1.ContainerEnvironment) *Handler {
	t.Helper()
	root := t.TempDir()
	writeTestChallenge(t, root)
	seedTestTaxonomy(t, root)
	database := testpostgres.New(t)
	if _, err := database.CreateUserWithAuth("u-demo", "demo", "hash", "totp"); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/namespaces/workspace-demo/pods/workspace/exec" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(metav1.Status{
				Status:  metav1.StatusFailure,
				Message: "test Kubernetes exec endpoint rejected request",
				Code:    http.StatusInternalServerError,
			})
			return
		}
		if r.URL.Path == "/apis/breakfix.dev/v1/namespaces/breakfix-system/vclusterenvironments" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(breakfixv1.VClusterEnvironmentList{
				TypeMeta: metav1.TypeMeta{APIVersion: "breakfix.dev/v1", Kind: "VClusterEnvironmentList"},
			})
			return
		}
		if r.URL.Path != "/apis/breakfix.dev/v1/namespaces/breakfix-system/containerenvironments" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(breakfixv1.ContainerEnvironmentList{
			TypeMeta: metav1.TypeMeta{APIVersion: "breakfix.dev/v1", Kind: "ContainerEnvironmentList"},
			Items:    environments,
		})
	}))
	t.Cleanup(server.Close)
	kubeconfig := filepath.Join(root, "kubeconfig")
	writeTestFile(t, kubeconfig, "apiVersion: v1\nclusters:\n- cluster:\n    server: "+server.URL+"\n  name: test\ncontexts:\n- context:\n    cluster: test\n    user: test\n  name: test\ncurrent-context: test\nkind: Config\nusers:\n- name: test\n  user: {}\n")
	client, err := k8s.New(kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	return NewHandler(database, client, config.Config{DataDir: root, CRDNamespace: "breakfix-system"})
}

func writeTestChallenge(t *testing.T, root string) {
	t.Helper()
	challengeDir := filepath.Join(root, "challenges", "demo")
	writeTestFile(t, filepath.Join(challengeDir, "challenge.yaml"), "id: demo\nsource_slug: demo\ntitle: Demo\ntype: script\nruntime: container\ndifficulty: easy\ndescription: demo\nimage: demo:v1\npublished_at: 2026-07-24T09:00:00Z\ncheckpoints:\n  - id: complete\n    title: Complete\n    description: Complete the task\n    hint: hints/complete.md\n")
	writeTestFile(t, filepath.Join(challengeDir, "Dockerfile"), "FROM breakfix-base:latest\n")
	writeTestFile(t, filepath.Join(challengeDir, "generate.sh"), "#!/bin/sh\n")
	writeTestFile(t, filepath.Join(challengeDir, "problem.md"), "problem\n")
	writeTestFile(t, filepath.Join(challengeDir, "solution.md"), "<!-- checkpoint: complete -->\nsolution\n")
	writeTestFile(t, filepath.Join(challengeDir, "hints", "complete.md"), "hint\n")
	writeTestFile(t, filepath.Join(challengeDir, "checks", "checkpoints.sh"), "#!/bin/sh\n")
	writeTestFile(t, filepath.Join(challengeDir, "answer.sh"), "#!/bin/sh\nexit 0\n")
}
