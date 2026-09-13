package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	breakfixv1 "github.com/breakfix/breakfix/api/v1"
	"github.com/breakfix/breakfix/internal/adapter/kubernetes"
	"github.com/breakfix/breakfix/internal/adapter/postgres"
	"github.com/breakfix/breakfix/internal/bootstrap/config"
	testpostgres "github.com/breakfix/breakfix/internal/testkit/postgres"
	"github.com/gin-gonic/gin"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const (
	testNodeEnvironmentCollection = "/apis/breakfix.dev/v1/namespaces/breakfix-system/nodeenvironments"
	testVK8sEnvironmentCollection = "/apis/breakfix.dev/v1/namespaces/breakfix-system/vk8senvironments"
)

type environmentAPITestState struct {
	mu sync.Mutex

	nodes              map[string]breakfixv1.NodeEnvironment
	listErr            bool
	deleteErr          bool
	createSuccesses    int
	deleteRequests     int
	blockInitialLists  bool
	initialListCalls   int
	initialListRelease chan struct{}
}

func newEnvironmentAPITestState() *environmentAPITestState {
	return &environmentAPITestState{nodes: make(map[string]breakfixv1.NodeEnvironment)}
}

func (s *environmentAPITestState) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == testNodeEnvironmentCollection:
		s.serveNodeCollection(w, r)
	case strings.HasPrefix(r.URL.Path, testNodeEnvironmentCollection+"/"):
		s.serveNode(w, r, strings.TrimPrefix(r.URL.Path, testNodeEnvironmentCollection+"/"))
	case r.URL.Path == testVK8sEnvironmentCollection && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, breakfixv1.VK8sEnvironmentList{TypeMeta: metav1.TypeMeta{APIVersion: "breakfix.dev/v1", Kind: "VK8sEnvironmentList"}})
	default:
		http.NotFound(w, r)
	}
}

func (s *environmentAPITestState) serveNodeCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.mu.Lock()
		if s.listErr {
			s.mu.Unlock()
			writeKubernetesError(w, http.StatusInternalServerError, metav1.StatusReasonInternalError, "control plane unavailable")
			return
		}
		items := make([]breakfixv1.NodeEnvironment, 0, len(s.nodes))
		for _, environment := range s.nodes {
			items = append(items, environment)
		}
		block := s.blockInitialLists && s.initialListCalls < 2
		if block {
			s.initialListCalls++
			if s.initialListCalls == 2 {
				close(s.initialListRelease)
			}
		}
		s.mu.Unlock()
		if block {
			<-s.initialListRelease
		}
		writeJSON(w, http.StatusOK, breakfixv1.NodeEnvironmentList{
			TypeMeta: metav1.TypeMeta{APIVersion: "breakfix.dev/v1", Kind: "NodeEnvironmentList"},
			Items:    items,
		})
	case http.MethodPost:
		var environment breakfixv1.NodeEnvironment
		if err := json.NewDecoder(r.Body).Decode(&environment); err != nil {
			writeKubernetesError(w, http.StatusBadRequest, metav1.StatusReasonBadRequest, err.Error())
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if _, exists := s.nodes[environment.Name]; exists {
			writeKubernetesError(w, http.StatusConflict, metav1.StatusReasonAlreadyExists, "environment already exists")
			return
		}
		s.createSuccesses++
		environment.UID = types.UID(fmt.Sprintf("environment-%d", s.createSuccesses))
		environment.Status.Environment.Phase = breakfixv1.EnvironmentReady
		s.nodes[environment.Name] = environment
		writeJSON(w, http.StatusCreated, environment)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *environmentAPITestState) serveNode(w http.ResponseWriter, r *http.Request, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	environment, exists := s.nodes[name]
	if !exists {
		writeKubernetesError(w, http.StatusNotFound, metav1.StatusReasonNotFound, "environment not found")
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, environment)
	case http.MethodDelete:
		s.deleteRequests++
		if s.deleteErr {
			writeKubernetesError(w, http.StatusInternalServerError, metav1.StatusReasonInternalError, "delete rejected")
			return
		}
		delete(s.nodes, name)
		writeJSON(w, http.StatusOK, metav1.Status{Status: metav1.StatusSuccess})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeKubernetesError(w http.ResponseWriter, status int, reason metav1.StatusReason, message string) {
	writeJSON(w, status, metav1.Status{Status: metav1.StatusFailure, Reason: reason, Message: message, Code: int32(status)})
}

func newEnvironmentLifecycleHandler(t *testing.T, state *environmentAPITestState) *Handler {
	t.Helper()
	root := t.TempDir()
	writeTestScenario(t, root)
	database := testpostgres.New(t)
	if _, err := database.Identity.CreateUserWithAuth("u-demo", "demo", "hash", "totp"); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(state)
	t.Cleanup(server.Close)
	kubeconfig := filepath.Join(root, "kubeconfig")
	writeTestFile(t, kubeconfig, "apiVersion: v1\nclusters:\n- cluster:\n    server: "+server.URL+"\n  name: test\ncontexts:\n- context:\n    cluster: test\n    user: test\n  name: test\ncurrent-context: test\nkind: Config\nusers:\n- name: test\n  user: {}\n")
	client, err := kubernetes.New(kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	return newHandlerForTest(t, database, client, config.Config{DataDir: root, CRDNamespace: "breakfix-system", CooldownMinutes: 1})
}

func scenarioRequest(t *testing.T, handler *Handler, method, action string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(method, "/api/operations/scenarios/demo/"+action, nil)
	ctx.Set("user_id", "u-demo")
	switch action {
	case "start":
		handler.StartScenario(ctx, "demo")
	case "reset":
		handler.ResetScenario(ctx, "demo")
	case "stop":
		handler.StopScenario(ctx, "demo")
	default:
		t.Fatalf("unknown scenario action %q", action)
	}
	return recorder
}

func testReadyEnvironment(name string) breakfixv1.NodeEnvironment {
	environment := testNodeEnvironment(name, breakfixv1.EnvironmentReady, nil)
	environment.UID = types.UID(name + "-uid")
	return environment
}

func TestStartScenarioSurfacesEnvironmentLookupFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	state := newEnvironmentAPITestState()
	state.listErr = true
	handler := newEnvironmentLifecycleHandler(t, state)

	recorder := scenarioRequest(t, handler, http.MethodPost, "start")

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("start status = %d, want 500: %s", recorder.Code, recorder.Body.String())
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.createSuccesses != 0 {
		t.Fatalf("start created an environment after lookup failure: %#v", state.nodes)
	}
}

func TestConcurrentStartScenarioCreatesOneEnvironment(t *testing.T) {
	gin.SetMode(gin.TestMode)
	state := newEnvironmentAPITestState()
	state.blockInitialLists = true
	state.initialListRelease = make(chan struct{})
	handler := newEnvironmentLifecycleHandler(t, state)

	responses := make(chan *httptest.ResponseRecorder, 2)
	go func() { responses <- scenarioRequest(t, handler, http.MethodPost, "start") }()
	go func() { responses <- scenarioRequest(t, handler, http.MethodPost, "start") }()
	first := <-responses
	second := <-responses

	if first.Code != http.StatusOK || second.Code != http.StatusOK {
		t.Fatalf("concurrent start responses = %d, %d", first.Code, second.Code)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.createSuccesses != 1 || len(state.nodes) != 1 {
		t.Fatalf("concurrent start created %d environments: %#v", state.createSuccesses, state.nodes)
	}
}

func TestResetScenarioDoesNotCreateWhenDeletionFails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	state := newEnvironmentAPITestState()
	state.deleteErr = true
	state.nodes["old-environment"] = testReadyEnvironment("old-environment")
	handler := newEnvironmentLifecycleHandler(t, state)

	recorder := scenarioRequest(t, handler, http.MethodPost, "reset")

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("reset status = %d, want 500: %s", recorder.Code, recorder.Body.String())
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.createSuccesses != 0 || len(state.nodes) != 1 || state.deleteRequests != 1 {
		t.Fatalf("reset changed environment state after failed deletion: creates=%d deletes=%d nodes=%#v", state.createSuccesses, state.deleteRequests, state.nodes)
	}
}

func TestResetAndStopRecordTerminalStateAfterDeletion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Run("reset", func(t *testing.T) {
		state := newEnvironmentAPITestState()
		state.nodes["old-environment"] = testReadyEnvironment("old-environment")
		handler := newEnvironmentLifecycleHandler(t, state)

		recorder := scenarioRequest(t, handler, http.MethodPost, "reset")
		if recorder.Code != http.StatusOK {
			t.Fatalf("reset status = %d: %s", recorder.Code, recorder.Body.String())
		}
		assertTerminalOutcome(t, handler.db.Environment, "old-environment-uid", postgres.AttemptReset)
		state.mu.Lock()
		defer state.mu.Unlock()
		if state.deleteRequests != 1 || state.createSuccesses != 1 || len(state.nodes) != 1 {
			t.Fatalf("reset lifecycle calls = deletes:%d creates:%d nodes:%#v", state.deleteRequests, state.createSuccesses, state.nodes)
		}
	})

	t.Run("stop", func(t *testing.T) {
		state := newEnvironmentAPITestState()
		state.nodes["active-environment"] = testReadyEnvironment("active-environment")
		handler := newEnvironmentLifecycleHandler(t, state)

		recorder := scenarioRequest(t, handler, http.MethodPost, "stop")
		if recorder.Code != http.StatusOK {
			t.Fatalf("stop status = %d: %s", recorder.Code, recorder.Body.String())
		}
		assertTerminalOutcome(t, handler.db.Environment, "active-environment-uid", postgres.AttemptStopped)
		state.mu.Lock()
		defer state.mu.Unlock()
		if state.deleteRequests != 1 || len(state.nodes) != 0 {
			t.Fatalf("stop lifecycle calls = deletes:%d nodes:%#v", state.deleteRequests, state.nodes)
		}
	})
}

func assertTerminalOutcome(t *testing.T, repository *postgres.EnvironmentRepository, environmentUID, want string) {
	t.Helper()
	history, err := repository.ListLearningHistory(context.Background(), "u-demo", postgres.LearningHistoryFilter{ScenarioIDs: []string{"demo"}}, 10, nil, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range history {
		if item.EnvironmentUID == environmentUID {
			if item.Outcome != want || item.EndedAt == nil {
				t.Fatalf("attempt %q = %#v, want outcome %q", environmentUID, item, want)
			}
			return
		}
	}
	t.Fatalf("attempt %q was not recorded: %#v", environmentUID, history)
}
