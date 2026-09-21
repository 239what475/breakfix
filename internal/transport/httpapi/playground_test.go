package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	runtimev2 "github.com/breakfix/breakfix/api/v2"
	api "github.com/breakfix/breakfix/internal/transport/httpapi/generated"
	"github.com/gin-gonic/gin"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Every playground test runs without a documentation library installed: the
// binding is the user alone and no library, page, or content identity
// participates in resolving the session.
func playgroundRequest(handler *Handler, method, action string, authenticated bool) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(method, "/api/playground"+action, nil)
	if authenticated {
		ctx.Set("user_id", "u-demo")
	}
	switch method + " " + action {
	case http.MethodGet + "":
		handler.GetPlayground(ctx)
	case http.MethodPost + "":
		handler.StartPlayground(ctx)
	case http.MethodPost + " /reset":
		handler.ResetPlayground(ctx)
	case http.MethodDelete + "":
		handler.StopPlayground(ctx)
	default:
		panic("unknown playground action " + action)
	}
	return recorder
}

func playgroundBody(t *testing.T, recorder *httptest.ResponseRecorder) api.PlaygroundEnvironment {
	t.Helper()
	var response api.PlaygroundEnvironment
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	return response
}

// setPlaygroundPhase simulates controller reconciliation by moving the stored
// environment to one concrete phase.
func (state *environmentAPITestState) setPlaygroundPhase(t *testing.T, name string, phase runtimev2.EnvironmentPhase) {
	t.Helper()
	state.mu.Lock()
	defer state.mu.Unlock()
	environment, exists := state.environments[name]
	if !exists {
		t.Fatalf("playground environment %q does not exist", name)
	}
	environment.Status.Phase = phase
	environment.Status.Operation = runtimev2.OperationNone
	state.environments[name] = environment
}

func TestPlaygroundSessionLifecycle(t *testing.T) {
	state := newEnvironmentAPITestState()
	handler := newEnvironmentLifecycleHandler(t, state)

	recorder := playgroundRequest(handler, http.MethodGet, "", true)
	if recorder.Code != http.StatusOK || playgroundBody(t, recorder).State != api.PlaygroundEnvironmentStateNone {
		t.Fatalf("initial state = %d %s", recorder.Code, recorder.Body.String())
	}

	recorder = playgroundRequest(handler, http.MethodPost, "", true)
	if recorder.Code != http.StatusOK {
		t.Fatalf("create = %d %s", recorder.Code, recorder.Body.String())
	}
	if response := playgroundBody(t, recorder); response.State != api.PlaygroundEnvironmentStateCreating || response.EnvironmentId == nil || *response.EnvironmentId == "" {
		t.Fatalf("created state = %#v", response)
	}
	name := playgroundEnvironmentName("u-demo")
	if state.lastCreated == nil || state.lastCreated.Name != name {
		t.Fatalf("deterministic environment name = %#v, want %q", state.lastCreated, name)
	}
	if state.lastCreated.Spec.BlankRuntime == nil || state.lastCreated.Spec.BlankRuntime.Provider != "k8s" {
		t.Fatalf("blank runtime spec = %#v", state.lastCreated.Spec)
	}
	if state.lastCreated.Spec.RunnableRevisionRef != nil {
		t.Fatalf("blank environment carries a runnable revision: %#v", state.lastCreated.Spec.RunnableRevisionRef)
	}
	if state.lastCreated.Labels["breakfix.dev/content-kind"] != environmentContentPlayground {
		t.Fatalf("content kind label = %#v", state.lastCreated.Labels)
	}
	if _, pinned := state.lastCreated.Labels["breakfix.dev/content-id"]; pinned {
		t.Fatalf("playground carries a content identity: %#v", state.lastCreated.Labels)
	}
	if _, pinned := state.lastCreated.Labels["breakfix.dev/content-revision"]; pinned {
		t.Fatalf("playground carries a content revision: %#v", state.lastCreated.Labels)
	}

	recorder = playgroundRequest(handler, http.MethodGet, "", true)
	if playgroundBody(t, recorder).State != api.PlaygroundEnvironmentStateCreating {
		t.Fatalf("pending state = %s", recorder.Body.String())
	}

	state.setPlaygroundPhase(t, name, runtimev2.PhaseReady)
	recorder = playgroundRequest(handler, http.MethodGet, "", true)
	if response := playgroundBody(t, recorder); response.State != api.PlaygroundEnvironmentStateReady || response.Runtime == nil || *response.Runtime != "k8s" {
		t.Fatalf("ready state = %#v", response)
	}

	recorder = playgroundRequest(handler, http.MethodDelete, "", true)
	if recorder.Code != http.StatusOK {
		t.Fatalf("close = %d %s", recorder.Code, recorder.Body.String())
	}
	recorder = playgroundRequest(handler, http.MethodGet, "", true)
	if playgroundBody(t, recorder).State != api.PlaygroundEnvironmentStateNone {
		t.Fatalf("closed state = %s", recorder.Body.String())
	}

	// Closing without a session stays a successful no-op.
	recorder = playgroundRequest(handler, http.MethodDelete, "", true)
	if recorder.Code != http.StatusOK {
		t.Fatalf("idempotent close = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestPlaygroundCreateIsIdempotentPerUser(t *testing.T) {
	state := newEnvironmentAPITestState()
	handler := newEnvironmentLifecycleHandler(t, state)

	for range 3 {
		recorder := playgroundRequest(handler, http.MethodPost, "", true)
		if recorder.Code != http.StatusOK {
			t.Fatalf("repeated create = %d %s", recorder.Code, recorder.Body.String())
		}
	}
	if state.createSuccesses != 1 {
		t.Fatalf("repeated create raced a new environment: creates=%d", state.createSuccesses)
	}
}

func TestPlaygroundResetFencesOnReady(t *testing.T) {
	state := newEnvironmentAPITestState()
	handler := newEnvironmentLifecycleHandler(t, state)

	if recorder := playgroundRequest(handler, http.MethodPost, "/reset", true); recorder.Code != http.StatusConflict {
		t.Fatalf("reset without session = %d %s", recorder.Code, recorder.Body.String())
	}

	playgroundRequest(handler, http.MethodPost, "", true)
	name := playgroundEnvironmentName("u-demo")
	if recorder := playgroundRequest(handler, http.MethodPost, "/reset", true); recorder.Code != http.StatusConflict {
		t.Fatalf("reset while creating = %d %s", recorder.Code, recorder.Body.String())
	}

	state.setPlaygroundPhase(t, name, runtimev2.PhaseReady)
	recorder := playgroundRequest(handler, http.MethodPost, "/reset", true)
	if recorder.Code != http.StatusOK || playgroundBody(t, recorder).State != api.PlaygroundEnvironmentStateCreating {
		t.Fatalf("ready reset = %d %s", recorder.Code, recorder.Body.String())
	}
	state.mu.Lock()
	nonce := state.environments[name].Spec.ResetNonce
	state.mu.Unlock()
	if nonce != 1 {
		t.Fatalf("reset nonce = %d, want 1", nonce)
	}

	// A failed session recreates from scratch on the next create.
	state.setPlaygroundPhase(t, name, runtimev2.PhaseFailed)
	recorder = playgroundRequest(handler, http.MethodGet, "", true)
	if playgroundBody(t, recorder).State != api.PlaygroundEnvironmentStateFailed {
		t.Fatalf("failed state = %s", recorder.Body.String())
	}
	if recorder := playgroundRequest(handler, http.MethodPost, "", true); recorder.Code != http.StatusOK {
		t.Fatalf("recreate after failure = %d %s", recorder.Code, recorder.Body.String())
	}
	if state.deleteRequests != 1 {
		t.Fatalf("failed environment was not cleared before recreate: deletes=%d", state.deleteRequests)
	}
}

func TestPlaygroundRequiresLogin(t *testing.T) {
	state := newEnvironmentAPITestState()
	handler := newEnvironmentLifecycleHandler(t, state)

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
		if recorder := playgroundRequest(handler, action.method, action.action, false); recorder.Code != http.StatusUnauthorized {
			t.Fatalf("unauthenticated %s %s = %d", action.method, action.action, recorder.Code)
		}
	}
}

func TestPlaygroundStateProjectsReclamationAsNone(t *testing.T) {
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
		if got := playgroundState(env); got != want {
			t.Fatalf("state(%s/%s) = %q, want %q", env.Phase, env.Operation, got, want)
		}
	}
}

func TestPlaygroundPollingRenewsPreparingSessionLease(t *testing.T) {
	state := newEnvironmentAPITestState()
	handler := newEnvironmentLifecycleHandler(t, state)

	playgroundRequest(handler, http.MethodPost, "", true)
	name := playgroundEnvironmentName("u-demo")

	// Age the lease: a stale idle deadline is what polling must push back.
	state.mu.Lock()
	environment := state.environments[name]
	environment.Spec.Lease.RenewedAt = metav1.NewTime(time.Now().UTC().Add(-10 * time.Minute))
	state.environments[name] = environment
	state.mu.Unlock()

	recorder := playgroundRequest(handler, http.MethodGet, "", true)
	if playgroundBody(t, recorder).State != api.PlaygroundEnvironmentStateCreating {
		t.Fatalf("preparing state = %s", recorder.Body.String())
	}
	state.mu.Lock()
	renewed := state.environments[name].Spec.Lease.RenewedAt.Time
	state.mu.Unlock()
	if renewed.Before(time.Now().UTC().Add(-time.Minute)) {
		t.Fatalf("preparing lease was not renewed: %s", renewed)
	}
}

func TestPlaygroundVisibleWhileProvisioningWithoutRuntimeProjection(t *testing.T) {
	state := newEnvironmentAPITestState()
	handler := newEnvironmentLifecycleHandler(t, state)

	playgroundRequest(handler, http.MethodPost, "", true)
	name := playgroundEnvironmentName("u-demo")

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

	recorder := playgroundRequest(handler, http.MethodGet, "", true)
	if playgroundBody(t, recorder).State != api.PlaygroundEnvironmentStateCreating {
		t.Fatalf("provisioning state = %s", recorder.Body.String())
	}
	state.mu.Lock()
	renewed := state.environments[name].Spec.Lease.RenewedAt.Time
	state.mu.Unlock()
	if renewed.Before(time.Now().UTC().Add(-time.Minute)) {
		t.Fatalf("provisioning lease was not renewed: %s", renewed)
	}
}
