package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	runtimev2 "github.com/breakfix/breakfix/api/v2"
	docsource "github.com/breakfix/breakfix/internal/adapter/documentation"
	api "github.com/breakfix/breakfix/internal/transport/httpapi/generated"
	"github.com/gin-gonic/gin"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// fakeBlankScenarioLibrary pins the library identity the blank scenario binds
// to; page reads are unused by the scenario endpoints.
type fakeBlankScenarioLibrary struct{}

func (fakeBlankScenarioLibrary) ReadDocumentPage(string) (docsource.DocumentPage, error) {
	return docsource.DocumentPage{}, nil
}
func (fakeBlankScenarioLibrary) DocumentPageTitle(string) string   { return "" }
func (fakeBlankScenarioLibrary) DocumentTitles() map[string]string { return nil }
func (fakeBlankScenarioLibrary) ReadDocumentTree(string) ([]docsource.DocumentTreeChild, error) {
	return nil, nil
}
func (fakeBlankScenarioLibrary) ReadDocumentAsset(string) ([]byte, string, error) {
	return nil, "", nil
}
func (fakeBlankScenarioLibrary) PinnedContext() docsource.DocumentContext {
	return docsource.DocumentContext{SourceID: "kubernetes-io", Commit: "commit-01", Language: "en"}
}

func newBlankScenarioHandler(t *testing.T, state *environmentAPITestState) *Handler {
	t.Helper()
	handler := newEnvironmentLifecycleHandler(t, state)
	handler.documentationLibrary = fakeBlankScenarioLibrary{}
	return handler
}

func blankScenarioRequest(handler *Handler, method, action string, authenticated bool) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(method, "/api/documentation/scenario"+action, nil)
	if authenticated {
		ctx.Set("user_id", "u-demo")
	}
	switch method + " " + action {
	case http.MethodGet + "":
		handler.GetDocumentationScenario(ctx)
	case http.MethodPost + "":
		handler.StartDocumentationScenario(ctx)
	case http.MethodPost + " /reset":
		handler.ResetDocumentationScenario(ctx)
	case http.MethodDelete + "":
		handler.StopDocumentationScenario(ctx)
	default:
		panic("unknown blank scenario action " + action)
	}
	return recorder
}

func blankScenarioBody(t *testing.T, recorder *httptest.ResponseRecorder) api.DocumentationScenarioEnvironment {
	t.Helper()
	var response api.DocumentationScenarioEnvironment
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	return response
}

// setBlankScenarioPhase simulates controller reconciliation by moving the
// stored environment to one concrete phase.
func (state *environmentAPITestState) setBlankScenarioPhase(t *testing.T, name string, phase runtimev2.EnvironmentPhase) {
	t.Helper()
	state.mu.Lock()
	defer state.mu.Unlock()
	environment, exists := state.environments[name]
	if !exists {
		t.Fatalf("blank scenario environment %q does not exist", name)
	}
	environment.Status.Phase = phase
	environment.Status.Operation = runtimev2.OperationNone
	state.environments[name] = environment
}

func blankScenarioEnvironmentName() string {
	token := blankScenarioToken("kubernetes-io", "commit-01", "en")
	return learningEnvironmentName("u-demo", environmentContentTarget{
		kind: environmentContentDocumentationBlank, id: token, revisionID: token, runtime: "k8s", blank: true,
	})
}

func TestBlankScenarioTokenIsDeterministicPerLibrary(t *testing.T) {
	first := blankScenarioToken("kubernetes-io", "commit-01", "en")
	if first != blankScenarioToken("kubernetes-io", "commit-01", "en") {
		t.Fatal("token is not deterministic for the same pinned library")
	}
	if first == blankScenarioToken("kubernetes-io", "commit-02", "en") || first == blankScenarioToken("another-source", "commit-01", "en") {
		t.Fatal("token does not bind to the full pinned identity")
	}
	if len(first) > 32 {
		t.Fatalf("token %q exceeds the label budget", first)
	}
}

func TestBlankScenarioSessionLifecycle(t *testing.T) {
	state := newEnvironmentAPITestState()
	handler := newBlankScenarioHandler(t, state)

	recorder := blankScenarioRequest(handler, http.MethodGet, "", true)
	if recorder.Code != http.StatusOK || blankScenarioBody(t, recorder).State != api.DocumentationScenarioEnvironmentStateNone {
		t.Fatalf("initial state = %d %s", recorder.Code, recorder.Body.String())
	}

	recorder = blankScenarioRequest(handler, http.MethodPost, "", true)
	if recorder.Code != http.StatusOK {
		t.Fatalf("create = %d %s", recorder.Code, recorder.Body.String())
	}
	if response := blankScenarioBody(t, recorder); response.State != api.DocumentationScenarioEnvironmentStateCreating || response.EnvironmentId == nil || *response.EnvironmentId == "" {
		t.Fatalf("created state = %#v", response)
	}
	name := blankScenarioEnvironmentName()
	if state.lastCreated == nil || state.lastCreated.Name != name {
		t.Fatalf("deterministic environment name = %#v, want %q", state.lastCreated, name)
	}
	if state.lastCreated.Spec.BlankRuntime == nil || state.lastCreated.Spec.BlankRuntime.Provider != "k8s" {
		t.Fatalf("blank runtime spec = %#v", state.lastCreated.Spec)
	}
	if state.lastCreated.Spec.RunnableRevisionRef.ID != "" {
		t.Fatalf("blank environment carries a runnable revision: %#v", state.lastCreated.Spec.RunnableRevisionRef)
	}

	recorder = blankScenarioRequest(handler, http.MethodGet, "", true)
	if blankScenarioBody(t, recorder).State != api.DocumentationScenarioEnvironmentStateCreating {
		t.Fatalf("pending state = %s", recorder.Body.String())
	}

	state.setBlankScenarioPhase(t, name, runtimev2.PhaseReady)
	recorder = blankScenarioRequest(handler, http.MethodGet, "", true)
	if response := blankScenarioBody(t, recorder); response.State != api.DocumentationScenarioEnvironmentStateReady || response.Runtime == nil || *response.Runtime != "k8s" {
		t.Fatalf("ready state = %#v", response)
	}

	recorder = blankScenarioRequest(handler, http.MethodDelete, "", true)
	if recorder.Code != http.StatusOK {
		t.Fatalf("close = %d %s", recorder.Code, recorder.Body.String())
	}
	recorder = blankScenarioRequest(handler, http.MethodGet, "", true)
	if blankScenarioBody(t, recorder).State != api.DocumentationScenarioEnvironmentStateNone {
		t.Fatalf("closed state = %s", recorder.Body.String())
	}

	// Closing without a session stays a successful no-op.
	recorder = blankScenarioRequest(handler, http.MethodDelete, "", true)
	if recorder.Code != http.StatusOK {
		t.Fatalf("idempotent close = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestBlankScenarioCreateIsIdempotentPerUser(t *testing.T) {
	state := newEnvironmentAPITestState()
	handler := newBlankScenarioHandler(t, state)

	for range 3 {
		recorder := blankScenarioRequest(handler, http.MethodPost, "", true)
		if recorder.Code != http.StatusOK {
			t.Fatalf("repeated create = %d %s", recorder.Code, recorder.Body.String())
		}
	}
	if state.createSuccesses != 1 {
		t.Fatalf("repeated create raced a new environment: creates=%d", state.createSuccesses)
	}
}

func TestBlankScenarioResetFencesOnReady(t *testing.T) {
	state := newEnvironmentAPITestState()
	handler := newBlankScenarioHandler(t, state)

	if recorder := blankScenarioRequest(handler, http.MethodPost, "/reset", true); recorder.Code != http.StatusConflict {
		t.Fatalf("reset without session = %d %s", recorder.Code, recorder.Body.String())
	}

	blankScenarioRequest(handler, http.MethodPost, "", true)
	name := blankScenarioEnvironmentName()
	if recorder := blankScenarioRequest(handler, http.MethodPost, "/reset", true); recorder.Code != http.StatusConflict {
		t.Fatalf("reset while creating = %d %s", recorder.Code, recorder.Body.String())
	}

	state.setBlankScenarioPhase(t, name, runtimev2.PhaseReady)
	recorder := blankScenarioRequest(handler, http.MethodPost, "/reset", true)
	if recorder.Code != http.StatusOK || blankScenarioBody(t, recorder).State != api.DocumentationScenarioEnvironmentStateCreating {
		t.Fatalf("ready reset = %d %s", recorder.Code, recorder.Body.String())
	}
	state.mu.Lock()
	nonce := state.environments[name].Spec.ResetNonce
	state.mu.Unlock()
	if nonce != 1 {
		t.Fatalf("reset nonce = %d, want 1", nonce)
	}

	// A failed session recreates from scratch on the next create.
	state.setBlankScenarioPhase(t, name, runtimev2.PhaseFailed)
	recorder = blankScenarioRequest(handler, http.MethodGet, "", true)
	if blankScenarioBody(t, recorder).State != api.DocumentationScenarioEnvironmentStateFailed {
		t.Fatalf("failed state = %s", recorder.Body.String())
	}
	if recorder := blankScenarioRequest(handler, http.MethodPost, "", true); recorder.Code != http.StatusOK {
		t.Fatalf("recreate after failure = %d %s", recorder.Code, recorder.Body.String())
	}
	if state.deleteRequests != 1 {
		t.Fatalf("failed environment was not cleared before recreate: deletes=%d", state.deleteRequests)
	}
}

func TestBlankScenarioRequiresLoginAndLibrary(t *testing.T) {
	state := newEnvironmentAPITestState()
	handler := newBlankScenarioHandler(t, state)

	actions := []struct {
		method string
		action string
	}{
		{http.MethodGet, ""},
		{http.MethodPost, ""},
		{http.MethodPost, "/reset"},
		{http.MethodDelete, ""},
	}
	for _, action := range actions {
		if recorder := blankScenarioRequest(handler, action.method, action.action, false); recorder.Code != http.StatusUnauthorized {
			t.Fatalf("unauthenticated %s %s = %d", action.method, action.action, recorder.Code)
		}
	}

	withoutLibrary := newEnvironmentLifecycleHandler(t, newEnvironmentAPITestState())
	if recorder := blankScenarioRequest(withoutLibrary, http.MethodGet, "", true); recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("state without library = %d", recorder.Code)
	}
}

func TestBlankScenarioStateProjectsReclamationAsNone(t *testing.T) {
	cases := map[*activeEnvironment]string{
		nil: "none",
		{Phase: runtimev2.PhaseDraining, Operation: runtimev2.OperationNone}:     "none",
		{Phase: runtimev2.PhaseReleased, Operation: runtimev2.OperationNone}:     "none",
		{Phase: runtimev2.PhaseReady, Operation: runtimev2.OperationResetting}:   "creating",
		{Phase: runtimev2.PhaseReady, Operation: runtimev2.OperationNone}:        "ready",
		{Phase: runtimev2.PhaseFailed, Operation: runtimev2.OperationNone}:       "failed",
		{Phase: runtimev2.PhasePending, Operation: runtimev2.OperationNone}:      "creating",
		{Phase: runtimev2.PhaseProvisioning, Operation: runtimev2.OperationNone}: "creating",
	}
	for env, want := range cases {
		if got := blankScenarioState(env); got != want {
			t.Fatalf("state(%s/%s) = %q, want %q", env.Phase, env.Operation, got, want)
		}
	}
}

func TestBlankScenarioPollingRenewsPreparingSessionLease(t *testing.T) {
	state := newEnvironmentAPITestState()
	handler := newBlankScenarioHandler(t, state)

	blankScenarioRequest(handler, http.MethodPost, "", true)
	name := blankScenarioEnvironmentName()

	// Age the lease: a stale idle deadline is what polling must push back.
	state.mu.Lock()
	environment := state.environments[name]
	environment.Spec.Lease.RenewedAt = metav1.NewTime(time.Now().UTC().Add(-10 * time.Minute))
	state.environments[name] = environment
	state.mu.Unlock()

	recorder := blankScenarioRequest(handler, http.MethodGet, "", true)
	if blankScenarioBody(t, recorder).State != api.DocumentationScenarioEnvironmentStateCreating {
		t.Fatalf("preparing state = %s", recorder.Body.String())
	}
	state.mu.Lock()
	renewed := state.environments[name].Spec.Lease.RenewedAt.Time
	state.mu.Unlock()
	if renewed.Before(time.Now().UTC().Add(-time.Minute)) {
		t.Fatalf("preparing lease was not renewed: %s", renewed)
	}
}

func TestBlankScenarioVisibleWhileProvisioningWithoutRuntimeProjection(t *testing.T) {
	state := newEnvironmentAPITestState()
	handler := newBlankScenarioHandler(t, state)

	blankScenarioRequest(handler, http.MethodPost, "", true)
	name := blankScenarioEnvironmentName()

	// Early provisioning: the controller has not observed the environment
	// yet, so the runtime provider projection is empty. The label-based
	// lookup is blind here; the session must stay visible by name and the
	// polling read must keep the idle lease alive.
	state.mu.Lock()
	environment := state.environments[name]
	environment.Status = runtimev2.RuntimeEnvironmentStatus{Phase: runtimev2.PhaseProvisioning}
	environment.Spec.Lease.RenewedAt = metav1.NewTime(time.Now().UTC().Add(-10 * time.Minute))
	state.environments[name] = environment
	state.mu.Unlock()

	recorder := blankScenarioRequest(handler, http.MethodGet, "", true)
	if blankScenarioBody(t, recorder).State != api.DocumentationScenarioEnvironmentStateCreating {
		t.Fatalf("provisioning state = %s", recorder.Body.String())
	}
	state.mu.Lock()
	renewed := state.environments[name].Spec.Lease.RenewedAt.Time
	state.mu.Unlock()
	if renewed.Before(time.Now().UTC().Add(-time.Minute)) {
		t.Fatalf("provisioning lease was not renewed: %s", renewed)
	}
}
