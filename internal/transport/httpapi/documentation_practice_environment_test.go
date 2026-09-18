package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	runtimev2 "github.com/breakfix/breakfix/api/v2"
	"github.com/breakfix/breakfix/internal/adapter/postgres"
	documentdomain "github.com/breakfix/breakfix/internal/domain/documentpractice"
	"github.com/breakfix/breakfix/internal/domain/runnable"
	"github.com/gin-gonic/gin"
)

func practiceTestRevision() documentdomain.PracticeRevision {
	return documentdomain.PracticeRevision{
		FormatVersion:       documentdomain.FormatVersion,
		ID:                  "practice-01",
		RunnableRevisionRef: runnable.RevisionReference{ID: "runnable-revision-01", Digest: testRunnableRevisionDigest},
		ReaderProjection: &documentdomain.ReaderProjection{
			Title: "Pod lifecycle", Objective: "Observe Pod state", Boundary: "One Pod",
			Steps: []string{"Apply the Pod manifest"}, Observations: []string{"Pod reaches Running"},
		},
	}
}

func newPracticeEnvironmentHandler(t *testing.T, state *environmentAPITestState, revision documentdomain.PracticeRevision, getErr error) *Handler {
	t.Helper()
	handler := newEnvironmentLifecycleHandler(t, state)
	handler.documentationReader = fakePracticeReader{
		revision: revision,
		getErr:   getErr,
		profile:  runnable.RunnableRevision{Spec: runnable.RunnableSpec{RuntimeProfile: runnable.RuntimeProfile{Runtime: runnable.RuntimeNode, BaseImage: "kindest/node"}}},
	}
	return handler
}

func practiceRequest(t *testing.T, handler *Handler, method, action string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(method, "/api/documentation/practices/practice-01"+action, nil)
	ctx.Set("user_id", "u-demo")
	switch action {
	case "/start":
		handler.StartDocumentationPracticeEnvironment(ctx)
	case "/environment":
		handler.GetDocumentationPracticeEnvironment(ctx)
	case "/stop":
		handler.StopDocumentationPracticeEnvironment(ctx)
	case "/reset":
		handler.ResetDocumentationPracticeEnvironment(ctx)
	default:
		t.Fatalf("unknown practice action %q", action)
	}
	return recorder
}

func TestStartDocumentationPracticeEnvironmentIsIdempotent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	state := newEnvironmentAPITestState()
	handler := newPracticeEnvironmentHandler(t, state, practiceTestRevision(), nil)

	if recorder := practiceRequest(t, handler, http.MethodPost, "/start"); recorder.Code != http.StatusOK {
		t.Fatalf("first start status = %d: %s", recorder.Code, recorder.Body.String())
	}
	if recorder := practiceRequest(t, handler, http.MethodPost, "/start"); recorder.Code != http.StatusOK {
		t.Fatalf("repeat start status = %d: %s", recorder.Code, recorder.Body.String())
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	if state.createSuccesses != 1 || len(state.environments) != 1 || state.lastCreated == nil {
		t.Fatalf("repeated start created %d environments: %#v", state.createSuccesses, state.environments)
	}
	created := state.lastCreated
	if created.Labels["breakfix.dev/content-kind"] != "documentation-practice" || created.Labels["breakfix.dev/content-id"] != "practice-01" || created.Labels["breakfix.dev/purpose"] != string(runtimev2.PurposeLearning) {
		t.Fatalf("practice environment labels = %#v", created.Labels)
	}
	if created.Spec.RunnableRevisionRef != (runtimev2.RunnableRevisionReference{ID: "runnable-revision-01", Digest: testRunnableRevisionDigest}) || created.Spec.Purpose != runtimev2.PurposeLearning {
		t.Fatalf("practice environment spec = %#v", created.Spec)
	}
	expectedName := learningEnvironmentName("u-demo", environmentContentTarget{
		kind: environmentContentDocumentationPractice, id: "practice-01", revisionID: "runnable-revision-01",
	})
	if created.Name != expectedName {
		t.Fatalf("practice environment name = %q, want deterministic %q", created.Name, expectedName)
	}
}

func TestConcurrentStartDocumentationPracticeCreatesOneEnvironment(t *testing.T) {
	gin.SetMode(gin.TestMode)
	state := newEnvironmentAPITestState()
	state.blockInitialLists = true
	state.initialListRelease = make(chan struct{})
	handler := newPracticeEnvironmentHandler(t, state, practiceTestRevision(), nil)

	var mu sync.Mutex
	codes := make([]int, 0, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			recorder := practiceRequest(t, handler, http.MethodPost, "/start")
			mu.Lock()
			defer mu.Unlock()
			codes = append(codes, recorder.Code)
		}()
	}
	wg.Wait()
	if codes[0] != http.StatusOK || codes[1] != http.StatusOK {
		t.Fatalf("concurrent practice start responses = %v", codes)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.createSuccesses != 1 || len(state.environments) != 1 || state.lastCreated == nil {
		t.Fatalf("concurrent practice start created %d environments: %#v", state.createSuccesses, state.environments)
	}
	if state.lastCreated.Labels["breakfix.dev/content-kind"] != "documentation-practice" {
		t.Fatalf("concurrent practice start labels = %#v", state.lastCreated.Labels)
	}
}

func TestGetDocumentationPracticeEnvironmentReportsPhaseAndNodes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	state := newEnvironmentAPITestState()
	handler := newPracticeEnvironmentHandler(t, state, practiceTestRevision(), nil)

	if recorder := practiceRequest(t, handler, http.MethodGet, "/environment"); recorder.Code != http.StatusNotFound {
		t.Fatalf("environment without a session status = %d, want 404: %s", recorder.Code, recorder.Body.String())
	}
	if recorder := practiceRequest(t, handler, http.MethodPost, "/start"); recorder.Code != http.StatusOK {
		t.Fatalf("start status = %d: %s", recorder.Code, recorder.Body.String())
	}
	recorder := practiceRequest(t, handler, http.MethodGet, "/environment")
	if recorder.Code != http.StatusOK {
		t.Fatalf("environment status = %d: %s", recorder.Code, recorder.Body.String())
	}
	var environment struct {
		Phase   string   `json:"phase"`
		Runtime string   `json:"runtime"`
		Nodes   []string `json:"nodes"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &environment); err != nil {
		t.Fatal(err)
	}
	if environment.Phase != string(runtimev2.PhaseReady) || environment.Runtime != "node" || len(environment.Nodes) != 1 || environment.Nodes[0] != "host" {
		t.Fatalf("environment projection = %#v", environment)
	}
}

func TestStopAndResetDocumentationPracticeEnvironmentLifecycle(t *testing.T) {
	gin.SetMode(gin.TestMode)
	state := newEnvironmentAPITestState()
	handler := newPracticeEnvironmentHandler(t, state, practiceTestRevision(), nil)

	if recorder := practiceRequest(t, handler, http.MethodPost, "/start"); recorder.Code != http.StatusOK {
		t.Fatalf("start status = %d: %s", recorder.Code, recorder.Body.String())
	}
	recorder := practiceRequest(t, handler, http.MethodPost, "/reset")
	if recorder.Code != http.StatusOK {
		t.Fatalf("reset status = %d: %s", recorder.Code, recorder.Body.String())
	}
	state.mu.Lock()
	if state.createSuccesses != 1 || state.deleteRequests != 0 {
		state.mu.Unlock()
		t.Fatalf("reset created or deleted environments: creates:%d deletes:%d", state.createSuccesses, state.deleteRequests)
	}
	updated := state.environments[learningEnvironmentName("u-demo", environmentContentTarget{kind: environmentContentDocumentationPractice, id: "practice-01", revisionID: "runnable-revision-01"})]
	state.mu.Unlock()
	if updated.Spec.ResetNonce != 1 {
		t.Fatalf("reset nonce = %d, want 1", updated.Spec.ResetNonce)
	}

	if recorder := practiceRequest(t, handler, http.MethodPost, "/stop"); recorder.Code != http.StatusOK {
		t.Fatalf("stop status = %d: %s", recorder.Code, recorder.Body.String())
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.environments) != 0 {
		t.Fatalf("stop left environments behind: %#v", state.environments)
	}
}

func TestDocumentationPracticeEnvironmentRejectsUnknownPractice(t *testing.T) {
	gin.SetMode(gin.TestMode)
	state := newEnvironmentAPITestState()
	handler := newPracticeEnvironmentHandler(t, state, documentdomain.PracticeRevision{}, postgres.ErrPublishedPracticeNotFound)

	for _, action := range []string{"/start", "/environment", "/stop", "/reset"} {
		recorder := practiceRequest(t, handler, http.MethodPost, action)
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want 404: %s", action, recorder.Code, recorder.Body.String())
		}
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.createSuccesses != 0 {
		t.Fatalf("unknown practice created environments: %#v", state.environments)
	}
}
