package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	breakfixv1 "github.com/breakfix/breakfix/apis/breakfix/v1"
	"github.com/breakfix/breakfix/internal/api"
	"github.com/breakfix/breakfix/internal/config"
	"github.com/breakfix/breakfix/internal/db"
	"github.com/breakfix/breakfix/internal/k8s"
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
	handler := newProgressTestHandler(t, []breakfixv1.ContainerEnvironment{{
		Spec: breakfixv1.CommonEnvironmentSpec{ChallengeRef: "demo", UserRef: "u-demo"},
		Status: breakfixv1.CommonEnvironmentStatus{
			Phase: breakfixv1.EnvironmentCompleted,
			Checkpoints: &breakfixv1.CheckpointStatus{Results: []breakfixv1.CheckpointResultStatus{{
				ID: "complete", Summary: "done", Passed: true,
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
	if progress.Checks == nil || len(*progress.Checks) != 1 || (*progress.Checks)[0].Passed == nil || !*(*progress.Checks)[0].Passed {
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
	writeGatewayTestFile(t, filepath.Join(challengeDir, "challenge.yaml"), "id: demo\ntitle: Demo\ntype: script\nruntime: container\ndifficulty: easy\ntags: [linux]\ndescription: demo\nimage: demo:v1\npublished_at: 2026-07-24T09:00:00Z\ncheckpoints:\n  - id: complete\n    title: Complete\n    description: Complete the task\n    hint: hints/complete.md\n")
	writeGatewayTestFile(t, filepath.Join(challengeDir, "Dockerfile"), "FROM breakfix-base:latest\n")
	writeGatewayTestFile(t, filepath.Join(challengeDir, "generate.sh"), "#!/bin/sh\n")
	writeGatewayTestFile(t, filepath.Join(challengeDir, "problem.md"), "# Problem\nRepair it.\n")
	writeGatewayTestFile(t, filepath.Join(challengeDir, "solution.md"), "# Solution\nRepair it this way.\n")
	writeGatewayTestFile(t, filepath.Join(challengeDir, "hints", "complete.md"), "Look at the service.\n")
	writeGatewayTestFile(t, filepath.Join(challengeDir, "checks", "checkpoints.sh"), "#!/bin/sh\n")
	writeGatewayTestFile(t, filepath.Join(challengeDir, "answer.sh"), "#!/bin/sh\nexit 0\n")

	database, err := db.New(filepath.Join(root, "breakfix.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
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
	if content.Problem == nil || *content.Problem != "# Problem\nRepair it.\n" {
		t.Fatalf("unexpected problem: %#v", content.Problem)
	}
	if content.Hints == nil || (*content.Hints)["complete"] != "Look at the service.\n" {
		t.Fatalf("unexpected hints: %#v", content.Hints)
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

	writeGatewayTestFile(t, filepath.Join(challengeDir, "challenge.yaml"), "id: demo\ntitle: Demo\ntype: script\nruntime: vcluster\ndifficulty: easy\ntags:\n  - kubernetes\ndescription: demo\nimage: demo:v1\npublished_at: 2026-07-24T09:00:00Z\ncheckpoints:\n  - id: complete\n    title: Complete\n    description: Complete the task\n    hint: hints/complete.md\n")
	writeGatewayTestFile(t, filepath.Join(challengeDir, "Dockerfile"), "FROM breakfix-k8s-base:latest\n")
	writeGatewayTestFile(t, filepath.Join(challengeDir, "generate.sh"), "#!/bin/sh\n")
	writeGatewayTestFile(t, filepath.Join(challengeDir, "problem.md"), "problem\n")
	writeGatewayTestFile(t, filepath.Join(challengeDir, "solution.md"), "solution\n")
	writeGatewayTestFile(t, filepath.Join(challengeDir, "hints", "complete.md"), "hint\n")
	writeGatewayTestFile(t, filepath.Join(challengeDir, "checks", "checkpoints.sh"), "#!/bin/sh\n")
	writeGatewayTestFile(t, filepath.Join(challengeDir, "answer.sh"), "#!/bin/sh\nexit 0\n")

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
	if resp.Challenges == nil || len(*resp.Challenges) != 1 {
		t.Fatalf("expected 1 challenge, got %#v", resp.Challenges)
	}
	got := (*resp.Challenges)[0]
	if got.Runtime == nil || *got.Runtime != "vcluster" {
		t.Fatalf("expected runtime vcluster, got %#v", got.Runtime)
	}
	if got.PublishedAt == nil || !got.PublishedAt.Equal(time.Date(2026, time.July, 24, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("unexpected published time: %#v", got.PublishedAt)
	}
	if got.Active != nil || got.Solved != nil || got.Progress != nil {
		t.Fatalf("guest catalog exposed personal state: %#v", got)
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
	if response.Challenges == nil || len(*response.Challenges) != 1 {
		t.Fatalf("unexpected challenges: %#v", response.Challenges)
	}
	got := (*response.Challenges)[0]
	if got.Active == nil || !*got.Active || got.Solved == nil || !*got.Solved {
		t.Fatalf("unexpected user state: %#v", got)
	}
	if got.Progress == nil || got.Progress.Passed == nil || got.Progress.Total == nil || *got.Progress.Passed != 1 || *got.Progress.Total != 1 {
		t.Fatalf("unexpected checkpoint progress: %#v", got.Progress)
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
	got := (*response.Challenges)[0]
	if got.Solved == nil || !*got.Solved || got.Active == nil || *got.Active || got.Progress != nil {
		t.Fatalf("unexpected durable completion state: %#v", got)
	}
}

func writeGatewayTestFile(t *testing.T, path, content string) {
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
	writeGatewayChallenge(t, root)
	database, err := db.New(filepath.Join(root, "breakfix.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
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
	writeGatewayTestFile(t, kubeconfig, "apiVersion: v1\nclusters:\n- cluster:\n    server: "+server.URL+"\n  name: test\ncontexts:\n- context:\n    cluster: test\n    user: test\n  name: test\ncurrent-context: test\nkind: Config\nusers:\n- name: test\n  user: {}\n")
	client, err := k8s.New(kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	return NewHandler(database, client, config.Config{DataDir: root, CRDNamespace: "breakfix-system"})
}

func writeGatewayChallenge(t *testing.T, root string) {
	t.Helper()
	challengeDir := filepath.Join(root, "challenges", "demo")
	writeGatewayTestFile(t, filepath.Join(challengeDir, "challenge.yaml"), "id: demo\ntitle: Demo\ntype: script\nruntime: container\ndifficulty: easy\ntags: [linux]\ndescription: demo\nimage: demo:v1\npublished_at: 2026-07-24T09:00:00Z\ncheckpoints:\n  - id: complete\n    title: Complete\n    description: Complete the task\n    hint: hints/complete.md\n")
	writeGatewayTestFile(t, filepath.Join(challengeDir, "Dockerfile"), "FROM breakfix-base:latest\n")
	writeGatewayTestFile(t, filepath.Join(challengeDir, "generate.sh"), "#!/bin/sh\n")
	writeGatewayTestFile(t, filepath.Join(challengeDir, "problem.md"), "problem\n")
	writeGatewayTestFile(t, filepath.Join(challengeDir, "solution.md"), "solution\n")
	writeGatewayTestFile(t, filepath.Join(challengeDir, "hints", "complete.md"), "hint\n")
	writeGatewayTestFile(t, filepath.Join(challengeDir, "checks", "checkpoints.sh"), "#!/bin/sh\n")
	writeGatewayTestFile(t, filepath.Join(challengeDir, "answer.sh"), "#!/bin/sh\nexit 0\n")
}
