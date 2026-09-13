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
	"github.com/breakfix/breakfix/internal/content/scenario"
	testpostgres "github.com/breakfix/breakfix/internal/testkit/postgres"
	api "github.com/breakfix/breakfix/internal/transport/httpapi/generated"
	"github.com/gin-gonic/gin"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestReadinessUsesMaterializedCatalogIntegrity(t *testing.T) {
	root := t.TempDir()
	writeTestScenario(t, root)
	handler := newHandlerForTest(t, testpostgres.New(t), nil, config.Config{DataDir: root})
	if err := handler.validateReadiness(context.Background()); err != nil {
		t.Fatalf("valid catalog readiness = %v", err)
	}
	writeTestFile(t, filepath.Join(root, "scenarios", "demo", testPublishedScenarioRevisionID, "solution.md"), "<!-- checkpoint: complete -->\nchanged\n")
	if err := handler.validateReadiness(context.Background()); err == nil || !errors.Is(err, appcatalog.ErrMaterializedIntegrity) {
		t.Fatalf("changed catalog readiness error = %v", err)
	}
}

func TestMySpaceScenarioContentSourceReflectsItsModule(t *testing.T) {
	operations := mySpaceScenario(scenario.Entry{ID: "chal-operations", Title: "Operations", Runtime: scenario.RuntimeNode, Type: scenario.ScenarioOperationsScenario})
	if operations.ContentSource != api.Operations {
		t.Fatalf("operations source = %q", operations.ContentSource)
	}
	documentation := mySpaceScenario(scenario.Entry{ID: "chal-documentation", Title: "Documentation", Runtime: scenario.RuntimeK8s, Type: scenario.ScenarioDocumentationExample})
	if documentation.ContentSource != api.Documentation {
		t.Fatalf("documentation source = %q", documentation.ContentSource)
	}
}

func TestGetScenarioProgressRejectsRequestsWithoutAnEnvironment(t *testing.T) {
	handler := newProgressTestHandler(t, nil)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/operations/scenarios/demo/progress", nil)
	ctx.Set("user_id", "u-demo")

	handler.GetScenarioProgress(ctx, "demo")

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

func TestGetScenarioProgressRejectsNonReadyEnvironment(t *testing.T) {
	handler := newProgressTestHandler(t, []breakfixv1.NodeEnvironment{
		testNodeEnvironment("environment-provisioning", breakfixv1.EnvironmentProvisioning, nil),
	})
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/operations/scenarios/demo/progress", nil)
	ctx.Set("user_id", "u-demo")

	handler.GetScenarioProgress(ctx, "demo")

	if recorder.Code != http.StatusConflict {
		t.Fatalf("expected 409 for non-ready environment, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestGetScenarioProgressSurfacesCheckpointRunnerFailures(t *testing.T) {
	handler := newProgressTestHandler(t, []breakfixv1.NodeEnvironment{
		testNodeEnvironment("environment-check-failed", breakfixv1.EnvironmentReady, &breakfixv1.CheckpointStatus{Error: "checkpoint runner exited with 1"}),
	})
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/operations/scenarios/demo/progress", nil)
	ctx.Set("user_id", "u-demo")

	handler.GetScenarioProgress(ctx, "demo")

	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for failed checkpoint runner, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestGetScenarioProgressReturnsControllerCheckpointSnapshot(t *testing.T) {
	firstPassedAt := metav1.NewTime(time.Date(2026, time.July, 28, 6, 0, 0, 0, time.UTC))
	handler := newProgressTestHandler(t, []breakfixv1.NodeEnvironment{
		testNodeEnvironment("environment-completed", breakfixv1.EnvironmentCompleted, &breakfixv1.CheckpointStatus{Results: []breakfixv1.CheckpointResultStatus{{
			ID: "complete", Summary: "done", Passed: true, FirstPassedAt: &firstPassedAt,
		}}}),
	})
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/operations/scenarios/demo/progress", nil)
	ctx.Set("user_id", "u-demo")

	handler.GetScenarioProgress(ctx, "demo")

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var progress api.ScenarioProgress
	if err := json.Unmarshal(recorder.Body.Bytes(), &progress); err != nil {
		t.Fatal(err)
	}
	if len(progress.Checks) != 1 || !progress.Checks[0].Passed || progress.Checks[0].FirstPassedAt == nil || !progress.Checks[0].FirstPassedAt.Equal(firstPassedAt.Time) {
		t.Fatalf("unexpected checkpoint snapshot: %#v", progress.Checks)
	}
}

func TestGetScenarioProgressRequiresLogin(t *testing.T) {
	handler := newHandlerForTest(t, nil, nil, config.Config{})
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/operations/scenarios/demo/progress", nil)

	handler.GetScenarioProgress(ctx, "demo")

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without login, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestGetScenarioContentReturnsPublishedAssetsForAuthenticatedUser(t *testing.T) {
	gin.SetMode(gin.TestMode)

	root := t.TempDir()
	scenariosDir := filepath.Join(root, "scenarios")
	scenarioDir := filepath.Join(scenariosDir, "demo", testPublishedScenarioRevisionID)
	writeTestFile(t, filepath.Join(scenarioDir, "scenario.yaml"), nodeTestManifest("Demo"))
	writeTestFile(t, filepath.Join(scenarioDir, "problem.md"), "# Problem\nRepair it.\n")
	writeTestFile(t, filepath.Join(scenarioDir, "solution.md"), "# Solution\n<!-- checkpoint: complete -->\nRepair it this way.\n")
	writeTestFile(t, filepath.Join(scenarioDir, "hints", "complete.md"), "Look at the service.\n")
	writeTestFile(t, filepath.Join(scenarioDir, "nodes", "host", "generate.sh"), "#!/bin/sh\n")
	writeTestFile(t, filepath.Join(scenarioDir, "nodes", "host", "reproduce.sh"), "#!/bin/sh\nprintf '{\"evidence\":[{\"id\":\"service-unavailable\",\"observed\":true,\"summary\":\"unavailable\"}]}'\n")
	writeTestFile(t, filepath.Join(scenarioDir, "nodes", "host", "checks.sh"), "#!/bin/sh\n")
	writeTestFile(t, filepath.Join(scenarioDir, "nodes", "host", "answer.sh"), "#!/bin/sh\nexit 0\n")
	database := testpostgres.New(t)
	if _, err := database.Identity.CreateUserWithAuth("u-demo", "demo", "hash", "totp"); err != nil {
		t.Fatal(err)
	}

	handler := newHandlerForTest(t, database, nil, config.Config{DataDir: root})
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/operations/scenarios/demo/content", nil)
	ctx.Set("user_id", "u-demo")

	handler.GetScenarioContent(ctx, "demo")

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var content api.ScenarioContent
	if err := json.Unmarshal(recorder.Body.Bytes(), &content); err != nil {
		t.Fatal(err)
	}
	if content.Problem == nil || *content.Problem != "# Problem\nRepair it.\n" {
		t.Fatalf("unexpected problem: %#v", content.Problem)
	}
	if content.Description != "demo" || content.Topology != "One host node." || content.Initialization != "generate.sh creates the broken state." || content.ReproductionObjective != "The service is unavailable." {
		t.Fatalf("scenario reproduction core = %#v", content)
	}
	if len(content.Versions) != 1 || content.Versions[0].Component != "fixture" || len(content.ReproductionEvidence) != 1 || content.ReproductionEvidence[0].Id != "service-unavailable" {
		t.Fatalf("scenario versions/evidence = %#v", content)
	}
	if content.Hints["complete"] != "Look at the service.\n" {
		t.Fatalf("unexpected hints: %#v", content.Hints)
	}
	if len(content.ScenarioTags) != 0 {
		t.Fatalf("scenario content projection = %#v", content)
	}
}

func TestGetScenarioContentReturnsReproductionCoreWithoutLearningAids(t *testing.T) {
	gin.SetMode(gin.TestMode)

	root := t.TempDir()
	scenarioDir := filepath.Join(root, "scenarios", "minimal", testPublishedScenarioRevisionID)
	writeTestFile(t, filepath.Join(scenarioDir, "scenario.yaml"), "id: minimal\nrevision_id: "+testPublishedScenarioRevisionID+"\nsource_slug: minimal\ntitle: Observe a failed service\nruntime: node\ndescription: A service was deployed but is not accepting requests.\nimage: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\ncontent_revision: sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\npublished_at: 2026-07-24T09:00:00Z\nversions:\n  - component: nginx\n    version: 1.27.0\ntopology: One host runs the service and its local client.\ninitialization: generate.sh installs the failed configuration.\nreproduction:\n  objective: A local request returns connection refused.\n  evidence:\n    - id: connection-refused\n      description: curl fails with connection refused.\n      node: host\nnodes:\n  - name: host\n    title: Host\ncheckpoints: []\n")
	writeTestFile(t, filepath.Join(scenarioDir, "nodes", "host", "generate.sh"), "#!/bin/sh\n")
	writeTestFile(t, filepath.Join(scenarioDir, "nodes", "host", "reproduce.sh"), "#!/bin/sh\nprintf '{\"evidence\":[{\"id\":\"connection-refused\",\"observed\":true,\"summary\":\"connection refused\"}]}'\n")
	database := testpostgres.New(t)
	if _, err := database.Identity.CreateUserWithAuth("u-demo", "demo", "hash", "totp"); err != nil {
		t.Fatal(err)
	}

	handler := newHandlerForTest(t, database, nil, config.Config{DataDir: root})
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/operations/scenarios/minimal/content", nil)
	ctx.Set("user_id", "u-demo")

	handler.GetScenarioContent(ctx, "minimal")

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var content api.ScenarioContent
	if err := json.Unmarshal(recorder.Body.Bytes(), &content); err != nil {
		t.Fatal(err)
	}
	if content.Description != "A service was deployed but is not accepting requests." || content.Topology == "" || content.Initialization == "" || content.ReproductionObjective == "" || len(content.Versions) != 1 || len(content.ReproductionEvidence) != 1 {
		t.Fatalf("missing reproduction core: %#v", content)
	}
	if content.Problem != nil || content.Solution != nil || len(content.Hints) != 0 || len(content.Checkpoints) != 0 {
		t.Fatalf("optional learning aids unexpectedly present: %#v", content)
	}
}

func TestListScenariosIncludesRuntime(t *testing.T) {
	gin.SetMode(gin.TestMode)

	root := t.TempDir()
	scenariosDir := filepath.Join(root, "scenarios")
	scenarioDir := filepath.Join(scenariosDir, "demo", testPublishedScenarioRevisionID)
	if err := os.MkdirAll(scenarioDir, 0755); err != nil {
		t.Fatal(err)
	}

	writeTestFile(t, filepath.Join(scenarioDir, "scenario.yaml"), "id: demo\nrevision_id: "+testPublishedScenarioRevisionID+"\nsource_slug: demo\ntitle: Demo\nruntime: k8s\ndescription: demo\nimage: registry.example/demo@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\ncontent_revision: sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\npublished_at: 2026-07-24T09:00:00Z\nversions:\n  - component: fixture\n    version: v1\ntopology: One Kubernetes cluster.\ninitialization: generate.sh removes the workload.\nreproduction:\n  objective: The workload is absent.\n  evidence:\n    - id: workload-absent\n      description: The workload is absent.\ncheckpoints:\n  - id: complete\n    title: Complete\n    description: Complete the task\n    hint: hints/complete.md\n")
	writeTestFile(t, filepath.Join(scenarioDir, "problem.md"), "problem\n")
	writeTestFile(t, filepath.Join(scenarioDir, "solution.md"), "<!-- checkpoint: complete -->\nsolution\n")
	writeTestFile(t, filepath.Join(scenarioDir, "hints", "complete.md"), "hint\n")
	writeTestFile(t, filepath.Join(scenarioDir, "k8s", "generate.sh"), "#!/bin/sh\n")
	writeTestFile(t, filepath.Join(scenarioDir, "k8s", "reproduce.sh"), "#!/bin/sh\nprintf '{\"evidence\":[{\"id\":\"workload-absent\",\"observed\":true,\"summary\":\"absent\"}]}'\n")
	writeTestFile(t, filepath.Join(scenarioDir, "k8s", "checks.sh"), "#!/bin/sh\n")
	writeTestFile(t, filepath.Join(scenarioDir, "k8s", "answer.sh"), "#!/bin/sh\nexit 0\n")
	cfg := config.Config{DataDir: root}
	database := testpostgres.New(t)
	handler := newHandlerForTest(t, database, nil, cfg)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/operations/scenarios", nil)

	handler.ListScenarios(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp api.ScenarioList
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Scenarios) != 1 {
		t.Fatalf("expected 1 scenario, got %#v", resp.Scenarios)
	}
	got := resp.Scenarios[0]
	if got.Runtime != api.ScenarioSummaryRuntimeK8s {
		t.Fatalf("expected runtime k8s, got %#v", got.Runtime)
	}
	if !got.PublishedAt.Equal(time.Date(2026, time.July, 24, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("unexpected published time: %#v", got.PublishedAt)
	}
	if got.Active != nil || got.Solved != nil || got.Progress != nil {
		t.Fatalf("guest catalog exposed personal state: %#v", got)
	}
	if len(got.ScenarioTags) != 0 {
		t.Fatalf("scenario summary = %#v", got)
	}
}

func TestListScenariosMergesCurrentProgressWithDurableCompletion(t *testing.T) {
	handler := newProgressTestHandler(t, []breakfixv1.NodeEnvironment{
		testNodeEnvironment("environment-active", breakfixv1.EnvironmentReady, &breakfixv1.CheckpointStatus{Results: []breakfixv1.CheckpointResultStatus{{
			ID: "complete", Passed: true, Summary: "done",
		}}}),
	})
	if err := handler.db.Environment.RecordScenarioCompletion(context.Background(), "u-demo", "demo", testPublishedScenarioRevisionID, "previous-environment", time.Date(2026, time.July, 24, 10, 30, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/operations/scenarios", nil)
	c.Set("user_id", "u-demo")
	handler.ListScenarios(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var response api.ScenarioList
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Scenarios) != 1 {
		t.Fatalf("unexpected scenarios: %#v", response.Scenarios)
	}
	got := response.Scenarios[0]
	if got.Active == nil || !*got.Active || got.Solved == nil || !*got.Solved {
		t.Fatalf("unexpected user state: %#v", got)
	}
	if got.Progress == nil || got.Progress.Passed != 1 || got.Progress.Total != 1 {
		t.Fatalf("unexpected checkpoint progress: %#v", got.Progress)
	}
}

func TestListScenariosShowsCompletedEnvironmentBeforeSQLProjection(t *testing.T) {
	handler := newProgressTestHandler(t, []breakfixv1.NodeEnvironment{
		testNodeEnvironment("environment-completed", breakfixv1.EnvironmentCompleted, &breakfixv1.CheckpointStatus{Results: []breakfixv1.CheckpointResultStatus{{
			ID: "complete", Passed: true, Summary: "done",
		}}}),
	})

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/operations/scenarios", nil)
	ctx.Set("user_id", "u-demo")
	handler.ListScenarios(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var response api.ScenarioList
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Scenarios) != 1 {
		t.Fatalf("unexpected scenarios: %#v", response.Scenarios)
	}
	got := response.Scenarios[0]
	if got.Solved == nil || !*got.Solved || got.Active == nil || *got.Active || got.Progress != nil {
		t.Fatalf("completed environment was not reflected immediately: %#v", got)
	}
}

func TestListScenariosKeepsCompletionAfterEnvironmentIsGone(t *testing.T) {
	handler := newProgressTestHandler(t, nil)
	if err := handler.db.Environment.RecordScenarioCompletion(context.Background(), "u-demo", "demo", testPublishedScenarioRevisionID, "completed-environment", time.Date(2026, time.July, 24, 10, 45, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/operations/scenarios", nil)
	c.Set("user_id", "u-demo")
	handler.ListScenarios(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var response api.ScenarioList
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	got := response.Scenarios[0]
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
	writeTestScenario(t, root)
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

func writeTestScenario(t *testing.T, root string) {
	t.Helper()
	scenarioDir := filepath.Join(root, "scenarios", "demo", testPublishedScenarioRevisionID)
	writeTestFile(t, filepath.Join(scenarioDir, "scenario.yaml"), nodeTestManifest("Demo"))
	writeTestFile(t, filepath.Join(scenarioDir, "problem.md"), "problem\n")
	writeTestFile(t, filepath.Join(scenarioDir, "solution.md"), "<!-- checkpoint: complete -->\nsolution\n")
	writeTestFile(t, filepath.Join(scenarioDir, "hints", "complete.md"), "hint\n")
	writeTestFile(t, filepath.Join(scenarioDir, "nodes", "host", "generate.sh"), "#!/bin/sh\n")
	writeTestFile(t, filepath.Join(scenarioDir, "nodes", "host", "reproduce.sh"), "#!/bin/sh\nprintf '{\"evidence\":[{\"id\":\"service-unavailable\",\"observed\":true,\"summary\":\"unavailable\"}]}'\n")
	writeTestFile(t, filepath.Join(scenarioDir, "nodes", "host", "checks.sh"), "#!/bin/sh\n")
	writeTestFile(t, filepath.Join(scenarioDir, "nodes", "host", "answer.sh"), "#!/bin/sh\nexit 0\n")
}

const testPublishedScenarioRevisionID = "chrev-aaaaaaaaaaaaaaaa"

func nodeTestManifest(title string) string {
	return "id: demo\nrevision_id: " + testPublishedScenarioRevisionID + "\nsource_slug: demo\ntitle: " + title + "\nruntime: node\ndescription: demo\nimage: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\ncontent_revision: sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\npublished_at: 2026-07-24T09:00:00Z\nversions:\n  - component: fixture\n    version: v1\ntopology: One host node.\ninitialization: generate.sh creates the broken state.\nreproduction:\n  objective: The service is unavailable.\n  evidence:\n    - id: service-unavailable\n      description: The service is unavailable.\n      node: host\nnodes:\n  - name: host\n    title: Host\ncheckpoints:\n  - id: complete\n    title: Complete\n    description: Complete the task\n    hint: hints/complete.md\n    node: host\n"
}

func testNodeEnvironment(name string, phase breakfixv1.EnvironmentPhase, checkpoints *breakfixv1.CheckpointStatus) breakfixv1.NodeEnvironment {
	return breakfixv1.NodeEnvironment{
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID(name + "-uid"), Labels: map[string]string{"breakfix.dev/user": "u-demo", "breakfix.dev/scenario": "demo"}},
		Spec: breakfixv1.NodeEnvironmentSpec{Environment: breakfixv1.EnvironmentSpec{
			Purpose: breakfixv1.EnvironmentPurposeLearning,
			Source:  breakfixv1.EnvironmentSourceSpec{Kind: breakfixv1.EnvironmentSourcePublished, Ref: "demo", Revision: testPublishedScenarioRevisionID},
			UserRef: "u-demo",
		}},
		Status: breakfixv1.NodeEnvironmentStatus{Environment: breakfixv1.EnvironmentStatus{Phase: phase, Checkpoints: checkpoints}},
	}
}
