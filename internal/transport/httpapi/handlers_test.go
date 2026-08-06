package httpapi

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

	breakfixv1 "github.com/breakfix/breakfix/api/v1"
	"github.com/breakfix/breakfix/internal/adapter/kubernetes"
	appcatalog "github.com/breakfix/breakfix/internal/application/catalog"
	"github.com/breakfix/breakfix/internal/bootstrap/config"
	testpostgres "github.com/breakfix/breakfix/internal/testkit/postgres"
	api "github.com/breakfix/breakfix/internal/transport/httpapi/generated"
	"github.com/gin-gonic/gin"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestReadinessUsesMaterializedCatalogIntegrity(t *testing.T) {
	root := t.TempDir()
	writeTestChallenge(t, root)
	handler := newHandlerForTest(t, testpostgres.New(t), nil, config.Config{DataDir: root})
	if err := handler.validateReadiness(context.Background()); err != nil {
		t.Fatalf("valid catalog readiness = %v", err)
	}
	writeTestFile(t, filepath.Join(root, "challenges", "demo", testPublishedChallengeRevisionID, "solution.md"), "<!-- checkpoint: complete -->\nchanged\n")
	if err := handler.validateReadiness(context.Background()); err == nil || !errors.Is(err, appcatalog.ErrMaterializedIntegrity) {
		t.Fatalf("changed catalog readiness error = %v", err)
	}
}

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
	handler := newProgressTestHandler(t, []breakfixv1.NodeEnvironment{
		testNodeEnvironment("environment-provisioning", breakfixv1.EnvironmentProvisioning, nil),
	})
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
	handler := newProgressTestHandler(t, []breakfixv1.NodeEnvironment{
		testNodeEnvironment("environment-check-failed", breakfixv1.EnvironmentReady, &breakfixv1.CheckpointStatus{Error: "checkpoint runner exited with 1"}),
	})
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
	handler := newProgressTestHandler(t, []breakfixv1.NodeEnvironment{
		testNodeEnvironment("environment-completed", breakfixv1.EnvironmentCompleted, &breakfixv1.CheckpointStatus{Results: []breakfixv1.CheckpointResultStatus{{
			ID: "complete", Summary: "done", Passed: true, FirstPassedAt: &firstPassedAt,
		}}}),
	})
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
	handler := newHandlerForTest(t, nil, nil, config.Config{})
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
	challengeDir := filepath.Join(challengesDir, "demo", testPublishedChallengeRevisionID)
	writeTestFile(t, filepath.Join(challengeDir, "challenge.yaml"), nodeTestManifest("Demo"))
	writeTestFile(t, filepath.Join(challengeDir, "problem.md"), "# Problem\nRepair it.\n")
	writeTestFile(t, filepath.Join(challengeDir, "solution.md"), "# Solution\n<!-- checkpoint: complete -->\nRepair it this way.\n")
	writeTestFile(t, filepath.Join(challengeDir, "hints", "complete.md"), "Look at the service.\n")
	writeTestFile(t, filepath.Join(challengeDir, "nodes", "host", "generate.sh"), "#!/bin/sh\n")
	writeTestFile(t, filepath.Join(challengeDir, "nodes", "host", "checks.sh"), "#!/bin/sh\n")
	writeTestFile(t, filepath.Join(challengeDir, "nodes", "host", "answer.sh"), "#!/bin/sh\nexit 0\n")
	database := testpostgres.New(t)
	if _, err := database.Identity.CreateUserWithAuth("u-demo", "demo", "hash", "totp"); err != nil {
		t.Fatal(err)
	}

	handler := newHandlerForTest(t, database, nil, config.Config{DataDir: root})
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
	if content.Roadmap.Domain.SourceRef != "test-catalog" || content.Roadmap.Topic.SourceRef != "test-catalog/repair" {
		t.Fatalf("roadmap topic projection = %#v", content.Roadmap)
	}
	if len(content.Roadmap.Tags) != 1 || content.Roadmap.Tags[0].SourceRef != "test" || content.Roadmap.Tags[0].Description != "Test fixture tag." {
		t.Fatalf("roadmap tag projection = %#v", content.Roadmap.Tags)
	}
}

func TestListChallengesIncludesRuntime(t *testing.T) {
	gin.SetMode(gin.TestMode)

	root := t.TempDir()
	challengesDir := filepath.Join(root, "challenges")
	challengeDir := filepath.Join(challengesDir, "demo", testPublishedChallengeRevisionID)
	if err := os.MkdirAll(challengeDir, 0755); err != nil {
		t.Fatal(err)
	}

	writeTestFile(t, filepath.Join(challengeDir, "challenge.yaml"), "id: demo\nrevision_id: "+testPublishedChallengeRevisionID+"\nsource_slug: demo\ntitle: Demo\nruntime: k8s\ndifficulty: easy\ndescription: demo\nimage: registry.example/demo@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\ncontent_revision: sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\npublished_at: 2026-07-24T09:00:00Z\ncheckpoints:\n  - id: complete\n    title: Complete\n    description: Complete the task\n    hint: hints/complete.md\n")
	writeTestFile(t, filepath.Join(challengeDir, "problem.md"), "problem\n")
	writeTestFile(t, filepath.Join(challengeDir, "solution.md"), "<!-- checkpoint: complete -->\nsolution\n")
	writeTestFile(t, filepath.Join(challengeDir, "hints", "complete.md"), "hint\n")
	writeTestFile(t, filepath.Join(challengeDir, "k8s", "generate.sh"), "#!/bin/sh\n")
	writeTestFile(t, filepath.Join(challengeDir, "k8s", "checks.sh"), "#!/bin/sh\n")
	writeTestFile(t, filepath.Join(challengeDir, "k8s", "answer.sh"), "#!/bin/sh\nexit 0\n")
	cfg := config.Config{DataDir: root}
	database := testpostgres.New(t)
	handler := newHandlerForTest(t, database, nil, cfg)

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
	if got.Runtime != api.ChallengeSummaryRuntimeK8s {
		t.Fatalf("expected runtime k8s, got %#v", got.Runtime)
	}
	if !got.PublishedAt.Equal(time.Date(2026, time.July, 24, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("unexpected published time: %#v", got.PublishedAt)
	}
	if got.Active != nil || got.Solved != nil || got.Progress != nil {
		t.Fatalf("guest catalog exposed personal state: %#v", got)
	}
	if got.Domain.SourceRef != "test-catalog" || got.Topic.SourceRef != "test-catalog/repair" {
		t.Fatalf("roadmap summary = %#v", got)
	}
	if len(got.Tags) != 1 || got.Tags[0].SourceRef != "test" || got.Tags[0].Title != "Test" {
		t.Fatalf("structured tags = %#v", got.Tags)
	}
}

func TestListChallengesMergesCurrentProgressWithDurableCompletion(t *testing.T) {
	handler := newProgressTestHandler(t, []breakfixv1.NodeEnvironment{
		testNodeEnvironment("environment-active", breakfixv1.EnvironmentReady, &breakfixv1.CheckpointStatus{Results: []breakfixv1.CheckpointResultStatus{{
			ID: "complete", Passed: true, Summary: "done",
		}}}),
	})
	if err := handler.db.Environment.RecordChallengeCompletion(context.Background(), "u-demo", "demo", testPublishedChallengeRevisionID, "previous-environment", time.Date(2026, time.July, 24, 10, 30, 0, 0, time.UTC)); err != nil {
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
	handler := newProgressTestHandler(t, []breakfixv1.NodeEnvironment{
		testNodeEnvironment("environment-completed", breakfixv1.EnvironmentCompleted, &breakfixv1.CheckpointStatus{Results: []breakfixv1.CheckpointResultStatus{{
			ID: "complete", Passed: true, Summary: "done",
		}}}),
	})

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
	if err := handler.db.Environment.RecordChallengeCompletion(context.Background(), "u-demo", "demo", testPublishedChallengeRevisionID, "completed-environment", time.Date(2026, time.July, 24, 10, 45, 0, 0, time.UTC)); err != nil {
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

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func newProgressTestHandler(t *testing.T, environments []breakfixv1.NodeEnvironment) *Handler {
	t.Helper()
	root := t.TempDir()
	writeTestChallenge(t, root)
	database := testpostgres.New(t)
	if _, err := database.Identity.CreateUserWithAuth("u-demo", "demo", "hash", "totp"); err != nil {
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
		if r.URL.Path == "/apis/breakfix.dev/v1/namespaces/breakfix-system/vk8senvironments" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(breakfixv1.VK8sEnvironmentList{
				TypeMeta: metav1.TypeMeta{APIVersion: "breakfix.dev/v1", Kind: "VK8sEnvironmentList"},
			})
			return
		}
		if r.URL.Path != "/apis/breakfix.dev/v1/namespaces/breakfix-system/nodeenvironments" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(breakfixv1.NodeEnvironmentList{
			TypeMeta: metav1.TypeMeta{APIVersion: "breakfix.dev/v1", Kind: "NodeEnvironmentList"},
			Items:    environments,
		})
	}))
	t.Cleanup(server.Close)
	kubeconfig := filepath.Join(root, "kubeconfig")
	writeTestFile(t, kubeconfig, "apiVersion: v1\nclusters:\n- cluster:\n    server: "+server.URL+"\n  name: test\ncontexts:\n- context:\n    cluster: test\n    user: test\n  name: test\ncurrent-context: test\nkind: Config\nusers:\n- name: test\n  user: {}\n")
	client, err := kubernetes.New(kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	return newHandlerForTest(t, database, client, config.Config{DataDir: root, CRDNamespace: "breakfix-system"})
}

func writeTestChallenge(t *testing.T, root string) {
	t.Helper()
	challengeDir := filepath.Join(root, "challenges", "demo", testPublishedChallengeRevisionID)
	writeTestFile(t, filepath.Join(challengeDir, "challenge.yaml"), nodeTestManifest("Demo"))
	writeTestFile(t, filepath.Join(challengeDir, "problem.md"), "problem\n")
	writeTestFile(t, filepath.Join(challengeDir, "solution.md"), "<!-- checkpoint: complete -->\nsolution\n")
	writeTestFile(t, filepath.Join(challengeDir, "hints", "complete.md"), "hint\n")
	writeTestFile(t, filepath.Join(challengeDir, "nodes", "host", "generate.sh"), "#!/bin/sh\n")
	writeTestFile(t, filepath.Join(challengeDir, "nodes", "host", "checks.sh"), "#!/bin/sh\n")
	writeTestFile(t, filepath.Join(challengeDir, "nodes", "host", "answer.sh"), "#!/bin/sh\nexit 0\n")
}

const testPublishedChallengeRevisionID = "chrev-aaaaaaaaaaaaaaaa"

func nodeTestManifest(title string) string {
	return "id: demo\nrevision_id: " + testPublishedChallengeRevisionID + "\nsource_slug: demo\ntitle: " + title + "\nruntime: node\ndifficulty: easy\ndescription: demo\nimage: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\ncontent_revision: sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\npublished_at: 2026-07-24T09:00:00Z\nnodes:\n  - name: host\n    title: Host\ncheckpoints:\n  - id: complete\n    title: Complete\n    description: Complete the task\n    hint: hints/complete.md\n    node: host\n"
}

func testNodeEnvironment(name string, phase breakfixv1.EnvironmentPhase, checkpoints *breakfixv1.CheckpointStatus) breakfixv1.NodeEnvironment {
	return breakfixv1.NodeEnvironment{
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID(name + "-uid"), Labels: map[string]string{"breakfix.dev/user": "u-demo", "breakfix.dev/challenge": "demo"}},
		Spec: breakfixv1.NodeEnvironmentSpec{Environment: breakfixv1.EnvironmentSpec{
			Purpose: breakfixv1.EnvironmentPurposeLearning,
			Source:  breakfixv1.EnvironmentSourceSpec{Kind: breakfixv1.EnvironmentSourcePublished, Ref: "demo", Revision: testPublishedChallengeRevisionID},
			UserRef: "u-demo",
		}},
		Status: breakfixv1.NodeEnvironmentStatus{Environment: breakfixv1.EnvironmentStatus{Phase: phase, Checkpoints: checkpoints}},
	}
}
