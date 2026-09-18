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

	runtimev2 "github.com/breakfix/breakfix/api/v2"
	"github.com/breakfix/breakfix/internal/adapter/kubernetes"
	"github.com/breakfix/breakfix/internal/adapter/postgres"
	"github.com/breakfix/breakfix/internal/bootstrap/config"
	"github.com/breakfix/breakfix/internal/domain/runnable"
	testpostgres "github.com/breakfix/breakfix/internal/testkit/postgres"
	"github.com/gin-gonic/gin"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const testRuntimeEnvironmentCollection = "/apis/breakfix.dev/v2/namespaces/breakfix-system/runtimeenvironments"

const testRunnableRevisionDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type staticOperationsBinding struct{ reference runnable.RevisionReference }

func (s staticOperationsBinding) ResolveOperationsRevisionBinding(context.Context, string) (runnable.RevisionReference, error) {
	return s.reference, nil
}

type environmentAPITestState struct {
	mu sync.Mutex

	environments       map[string]runtimev2.RuntimeEnvironment
	listErr            bool
	deleteErr          bool
	createSuccesses    int
	deleteRequests     int
	deleteUIDs         []types.UID
	lastCreated        *runtimev2.RuntimeEnvironment
	blockInitialLists  bool
	initialListCalls   int
	initialListRelease chan struct{}
}

func newEnvironmentAPITestState() *environmentAPITestState {
	return &environmentAPITestState{environments: make(map[string]runtimev2.RuntimeEnvironment)}
}

func (s *environmentAPITestState) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == testRuntimeEnvironmentCollection:
		s.serveCollection(w, r)
	case strings.HasPrefix(r.URL.Path, testRuntimeEnvironmentCollection+"/"):
		s.serveEnvironment(w, r, strings.TrimPrefix(r.URL.Path, testRuntimeEnvironmentCollection+"/"))
	default:
		http.NotFound(w, r)
	}
}

func (s *environmentAPITestState) serveCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.mu.Lock()
		if s.listErr {
			s.mu.Unlock()
			writeKubernetesError(w, http.StatusInternalServerError, metav1.StatusReasonInternalError, "control plane unavailable")
			return
		}
		items := make([]runtimev2.RuntimeEnvironment, 0, len(s.environments))
		for _, environment := range s.environments {
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
		writeJSON(w, http.StatusOK, runtimev2.RuntimeEnvironmentList{TypeMeta: metav1.TypeMeta{APIVersion: "breakfix.dev/v2", Kind: "RuntimeEnvironmentList"}, Items: items})
	case http.MethodPost:
		var environment runtimev2.RuntimeEnvironment
		if err := json.NewDecoder(r.Body).Decode(&environment); err != nil {
			writeKubernetesError(w, http.StatusBadRequest, metav1.StatusReasonBadRequest, err.Error())
			return
		}
		// The real API server rejects CRs whose label values exceed 63 bytes;
		// mirror that so environment metadata stays within the limit.
		for _, value := range environment.Labels {
			if len(value) > 63 {
				writeKubernetesError(w, http.StatusUnprocessableEntity, metav1.StatusReasonInvalid, fmt.Sprintf("metadata.labels: Invalid value: %q: must be no more than 63 bytes", value))
				return
			}
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if _, exists := s.environments[environment.Name]; exists {
			writeKubernetesError(w, http.StatusConflict, metav1.StatusReasonAlreadyExists, "environment already exists")
			return
		}
		s.createSuccesses++
		environment.UID = types.UID(fmt.Sprintf("environment-%d", s.createSuccesses))
		environment.Status = runtimev2.RuntimeEnvironmentStatus{Phase: runtimev2.PhaseReady, Runtime: runtimev2.RuntimeStatus{
			Provider: "node", ResourceRefs: []runtimev2.ResourceReference{{Provider: "incus", Kind: "project", ID: "project"}, {Provider: "incus", Kind: "network", ID: "network"}, {Provider: "incus", Kind: "acl", ID: "acl"}, {Provider: "incus", Kind: "profile", ID: "profile"}, {Provider: "incus", Kind: "instance:host", ID: "host"}},
		}}
		s.environments[environment.Name] = environment
		created := environment.DeepCopy()
		s.lastCreated = created
		writeJSON(w, http.StatusCreated, environment)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *environmentAPITestState) serveEnvironment(w http.ResponseWriter, r *http.Request, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	environment, exists := s.environments[name]
	if !exists {
		writeKubernetesError(w, http.StatusNotFound, metav1.StatusReasonNotFound, "environment not found")
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, environment)
	case http.MethodDelete:
		s.deleteRequests++
		var options metav1.DeleteOptions
		if err := json.NewDecoder(r.Body).Decode(&options); err != nil {
			writeKubernetesError(w, http.StatusBadRequest, metav1.StatusReasonBadRequest, err.Error())
			return
		}
		if options.Preconditions == nil || options.Preconditions.UID == nil {
			writeKubernetesError(w, http.StatusBadRequest, metav1.StatusReasonBadRequest, "missing UID precondition")
			return
		}
		s.deleteUIDs = append(s.deleteUIDs, *options.Preconditions.UID)
		if s.deleteErr {
			writeKubernetesError(w, http.StatusInternalServerError, metav1.StatusReasonInternalError, "delete rejected")
			return
		}
		if *options.Preconditions.UID != environment.UID {
			writeKubernetesError(w, http.StatusConflict, metav1.StatusReasonConflict, "UID changed")
			return
		}
		delete(s.environments, name)
		writeJSON(w, http.StatusOK, metav1.Status{Status: metav1.StatusSuccess})
	case http.MethodPut:
		var updated runtimev2.RuntimeEnvironment
		if err := json.NewDecoder(r.Body).Decode(&updated); err != nil {
			writeKubernetesError(w, http.StatusBadRequest, metav1.StatusReasonBadRequest, err.Error())
			return
		}
		if updated.UID != environment.UID {
			writeKubernetesError(w, http.StatusConflict, metav1.StatusReasonConflict, "environment UID changed during update")
			return
		}
		s.environments[name] = updated
		writeJSON(w, http.StatusOK, updated)
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
	if _, err := database.Identity.CreateUserWithAuth(context.Background(), "u-demo", "demo", "hash", "totp"); err != nil {
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
	handler := newHandlerForTest(t, database, client, config.Config{DataDir: root, CRDNamespace: "breakfix-system", CooldownMinutes: 1})
	handler.runnableBindings = staticOperationsBinding{reference: runnable.RevisionReference{ID: "rr-demo", Digest: testRunnableRevisionDigest}}
	return handler
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

func testReadyEnvironment(name string) runtimev2.RuntimeEnvironment {
	return runtimev2.RuntimeEnvironment{
		// A real API server always returns objects with their TypeMeta; the
		// client's scheme decoder rejects responses without it.
		TypeMeta: metav1.TypeMeta{APIVersion: "breakfix.dev/v2", Kind: "RuntimeEnvironment"},
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID(name + "-uid"), Labels: map[string]string{
			"breakfix.dev/user": "u-demo", "breakfix.dev/content-kind": "operations", "breakfix.dev/content-id": "demo", "breakfix.dev/content-revision": testPublishedScenarioRevisionID,
		}},
		Spec:   runtimev2.RuntimeEnvironmentSpec{RunnableRevisionRef: runtimev2.RunnableRevisionReference{ID: "rr-demo", Digest: testRunnableRevisionDigest}, Purpose: runtimev2.PurposeLearning, Lease: runtimev2.LeaseSpec{RenewedAt: metav1.Now()}},
		Status: runtimev2.RuntimeEnvironmentStatus{Phase: runtimev2.PhaseReady, Runtime: runtimev2.RuntimeStatus{Provider: "node"}},
	}
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
		t.Fatalf("start created an environment after lookup failure: %#v", state.environments)
	}
}

func TestConcurrentStartScenarioCreatesOneV2Environment(t *testing.T) {
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
	if state.createSuccesses != 1 || len(state.environments) != 1 || state.lastCreated == nil {
		t.Fatalf("concurrent start created %d environments: %#v", state.createSuccesses, state.environments)
	}
	if state.lastCreated.Spec.RunnableRevisionRef != (runtimev2.RunnableRevisionReference{ID: "rr-demo", Digest: testRunnableRevisionDigest}) || state.lastCreated.Spec.Purpose != runtimev2.PurposeLearning {
		t.Fatalf("created v2 environment spec = %#v", state.lastCreated.Spec)
	}
}

func TestServerMarksOnlyBoundVerificationEnvironmentReleasable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	state := newEnvironmentAPITestState()
	verification := testReadyEnvironment("verification-environment")
	verification.Spec.Purpose = runtimev2.PurposeVerification
	verification.Labels = nil
	state.environments[verification.Name] = verification
	learning := testReadyEnvironment("learning-environment")
	state.environments[learning.Name] = learning
	handler := newEnvironmentLifecycleHandler(t, state)

	if err := handler.markVerificationEnvironmentReleasable(context.Background(), string(verification.UID), verification.Spec.RunnableRevisionRef.Digest); err != nil {
		t.Fatalf("mark verification environment releasable: %v", err)
	}
	if err := handler.markVerificationEnvironmentReleasable(context.Background(), string(learning.UID), learning.Spec.RunnableRevisionRef.Digest); err == nil {
		t.Fatal("learning environment was accepted for verification release")
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.environments[verification.Name].Spec.Lease.ReleaseAt == nil {
		t.Fatalf("verification environment did not receive releaseAt: %#v", state.environments[verification.Name].Spec)
	}
	if state.environments[learning.Name].Spec.Lease.ReleaseAt != nil {
		t.Fatalf("learning environment was modified by verification release: %#v", state.environments[learning.Name].Spec)
	}
}

func TestResetScenarioUsesSameUIDAndIncrementsNonce(t *testing.T) {
	gin.SetMode(gin.TestMode)
	state := newEnvironmentAPITestState()
	state.environments["old-environment"] = testReadyEnvironment("old-environment")
	handler := newEnvironmentLifecycleHandler(t, state)

	recorder := scenarioRequest(t, handler, http.MethodPost, "reset")
	if recorder.Code != http.StatusOK {
		t.Fatalf("reset status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	updated := state.environments["old-environment"]
	if state.createSuccesses != 0 || state.deleteRequests != 0 || updated.UID != types.UID("old-environment-uid") || updated.Spec.ResetNonce != 1 {
		t.Fatalf("reset lifecycle did not preserve the v2 identity: creates:%d deletes:%d environment:%#v", state.createSuccesses, state.deleteRequests, updated)
	}
}

func TestResetPreservesAttemptAndStopRecordsTerminalState(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Run("reset", func(t *testing.T) {
		state := newEnvironmentAPITestState()
		state.environments["old-environment"] = testReadyEnvironment("old-environment")
		handler := newEnvironmentLifecycleHandler(t, state)

		recorder := scenarioRequest(t, handler, http.MethodPost, "reset")
		if recorder.Code != http.StatusOK {
			t.Fatalf("reset status = %d: %s", recorder.Code, recorder.Body.String())
		}
		state.mu.Lock()
		defer state.mu.Unlock()
		environment := state.environments["old-environment"]
		if state.deleteRequests != 0 || state.createSuccesses != 0 || len(state.environments) != 1 || environment.Spec.ResetNonce != 1 {
			t.Fatalf("reset lifecycle calls = deletes:%d creates:%d environment:%#v", state.deleteRequests, state.createSuccesses, environment)
		}
		history, err := handler.db.Environment.ListLearningHistory(context.Background(), "u-demo", postgres.LearningHistoryFilter{ScenarioIDs: []string{"demo"}}, 10, nil, time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		if len(history) != 0 {
			t.Fatalf("reset must not close the stable environment attempt: %#v", history)
		}
	})

	t.Run("stop", func(t *testing.T) {
		state := newEnvironmentAPITestState()
		state.environments["active-environment"] = testReadyEnvironment("active-environment")
		handler := newEnvironmentLifecycleHandler(t, state)

		recorder := scenarioRequest(t, handler, http.MethodPost, "stop")
		if recorder.Code != http.StatusOK {
			t.Fatalf("stop status = %d: %s", recorder.Code, recorder.Body.String())
		}
		assertTerminalOutcome(t, handler.db.Environment, "active-environment-uid", postgres.AttemptStopped)
		state.mu.Lock()
		defer state.mu.Unlock()
		if state.deleteRequests != 1 || len(state.environments) != 0 {
			t.Fatalf("stop lifecycle calls = deletes:%d environments:%#v", state.deleteRequests, state.environments)
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
