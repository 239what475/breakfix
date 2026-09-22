package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	runtimev2 "github.com/breakfix/breakfix/api/v2"
	api "github.com/breakfix/breakfix/internal/transport/httpapi/generated"
	"github.com/gin-gonic/gin"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
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
	case http.MethodGet + " ":
		handler.GetPlayground(ctx)
	case http.MethodPost + " ":
		handler.StartPlayground(ctx)
	case http.MethodPost + " /reset":
		handler.ResetPlayground(ctx)
	case http.MethodDelete + " ":
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

// testPlaygroundEnvironment builds a stored playground CR for any user: the
// content-kind label and blank arm carry no content identity, exactly what
// the capacity counter and the My space projection read back.
func testPlaygroundEnvironment(name, user string, phase runtimev2.EnvironmentPhase) runtimev2.RuntimeEnvironment {
	return runtimev2.RuntimeEnvironment{
		TypeMeta: metav1.TypeMeta{APIVersion: "breakfix.dev/v2", Kind: "RuntimeEnvironment"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "breakfix-system", UID: types.UID(name + "-uid"), Labels: map[string]string{
			"breakfix.dev/user": user, "breakfix.dev/content-kind": environmentContentPlayground,
		}},
		Spec:   runtimev2.RuntimeEnvironmentSpec{BlankRuntime: &runtimev2.BlankRuntimeSpec{Provider: "k8s"}, Purpose: runtimev2.PurposeLearning},
		Status: runtimev2.RuntimeEnvironmentStatus{Phase: phase, Runtime: runtimev2.RuntimeStatus{Provider: "k8s"}},
	}
}

func TestPlaygroundCreatePassesBelowCapacity(t *testing.T) {
	state := newEnvironmentAPITestState()
	handler := newEnvironmentLifecycleHandler(t, state)
	handler.playgroundMaxActive = 2

	other := testPlaygroundEnvironment(playgroundEnvironmentName("u-other"), "u-other", runtimev2.PhaseReady)
	state.mu.Lock()
	state.environments[other.Name] = other
	state.mu.Unlock()

	recorder := playgroundRequest(handler, http.MethodPost, "", true)
	if recorder.Code != http.StatusOK {
		t.Fatalf("create below capacity = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestPlaygroundCreateRejectedAtCapacity(t *testing.T) {
	state := newEnvironmentAPITestState()
	handler := newEnvironmentLifecycleHandler(t, state)
	handler.playgroundMaxActive = 1

	other := testPlaygroundEnvironment(playgroundEnvironmentName("u-other"), "u-other", runtimev2.PhaseReady)
	state.mu.Lock()
	state.environments[other.Name] = other
	state.mu.Unlock()

	// The gate only rejects creation: reads, and the state they report, stay
	// available for everyone else.
	recorder := playgroundRequest(handler, http.MethodGet, "", true)
	if recorder.Code != http.StatusOK || playgroundBody(t, recorder).State != api.PlaygroundEnvironmentStateNone {
		t.Fatalf("read at capacity = %d %s", recorder.Code, recorder.Body.String())
	}

	recorder = playgroundRequest(handler, http.MethodPost, "", true)
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("create at capacity = %d %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "playground is at capacity") {
		t.Fatalf("capacity error = %s", recorder.Body.String())
	}
	state.mu.Lock()
	creates := state.createSuccesses
	state.mu.Unlock()
	if creates != 0 {
		t.Fatalf("capacity gate reached the create path: creates=%d", creates)
	}

	// Draining sessions still occupy their resources, so they still count.
	state.mu.Lock()
	draining := other.DeepCopy()
	draining.Status.Phase = runtimev2.PhaseDraining
	state.environments[draining.Name] = *draining
	state.mu.Unlock()
	if recorder = playgroundRequest(handler, http.MethodPost, "", true); recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("create while draining = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestPlaygroundCapacityCountsOnlyLiveSessions(t *testing.T) {
	cases := map[string]func(env *runtimev2.RuntimeEnvironment){
		"failed":   func(env *runtimev2.RuntimeEnvironment) { env.Status.Phase = runtimev2.PhaseFailed },
		"released": func(env *runtimev2.RuntimeEnvironment) { env.Status.Phase = runtimev2.PhaseReleased },
		"still-deleting": func(env *runtimev2.RuntimeEnvironment) {
			env.Status.Phase = runtimev2.PhaseReady
			env.DeletionTimestamp = &metav1.Time{Time: time.Now().UTC()}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			state := newEnvironmentAPITestState()
			handler := newEnvironmentLifecycleHandler(t, state)
			handler.playgroundMaxActive = 1

			other := testPlaygroundEnvironment(playgroundEnvironmentName("u-other"), "u-other", runtimev2.PhaseReady)
			mutate(&other)
			state.mu.Lock()
			state.environments[other.Name] = other
			state.mu.Unlock()

			recorder := playgroundRequest(handler, http.MethodPost, "", true)
			if recorder.Code != http.StatusOK {
				t.Fatalf("create past a %s session = %d %s", name, recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestMySpaceListsThePlaygroundSession(t *testing.T) {
	state := newEnvironmentAPITestState()
	handler := newEnvironmentLifecycleHandler(t, state)

	playground := testPlaygroundEnvironment(playgroundEnvironmentName("u-demo"), "u-demo", runtimev2.PhaseProvisioning)
	// metav1.Time crosses the fake API server as RFC3339 seconds, so the
	// seeded expiry must already be second-truncated to survive the read.
	expiry := metav1.NewTime(time.Now().UTC().Add(30 * time.Minute).Truncate(time.Second))
	playground.Status.Lifecycle.ExpiresAt = &expiry
	state.mu.Lock()
	state.environments[playground.Name] = playground
	state.mu.Unlock()

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/me/space", nil)
	ctx.Set("user_id", "u-demo")
	handler.GetMySpace(ctx)
	if recorder.Code != http.StatusOK {
		t.Fatalf("my space = %d %s", recorder.Code, recorder.Body.String())
	}
	var space api.MySpace
	if err := json.NewDecoder(recorder.Body).Decode(&space); err != nil {
		t.Fatal(err)
	}
	if len(space.ActiveEnvironments) != 1 {
		t.Fatalf("active environments = %#v", space.ActiveEnvironments)
	}
	entry := space.ActiveEnvironments[0]
	if entry.Kind != api.MySpaceActiveEnvironmentKindPlayground || entry.EnvironmentId != playground.Name {
		t.Fatalf("playground entry = %#v", entry)
	}
	if entry.Scenario != nil || entry.CheckpointProgress != nil {
		t.Fatalf("playground entry carries content progress: %#v", entry)
	}
	if entry.Runtime != api.MySpaceActiveEnvironmentRuntime("k8s") || entry.Phase != string(runtimev2.PhaseProvisioning) {
		t.Fatalf("playground projection = %#v", entry)
	}
	if entry.ExpiresAt == nil || !entry.ExpiresAt.Equal(expiry.Time) {
		t.Fatalf("playground expiry = %#v", entry.ExpiresAt)
	}
	if space.Summary.InProgressEnvironmentCount != 1 {
		t.Fatalf("in-progress count = %d", space.Summary.InProgressEnvironmentCount)
	}
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

	// The generation rides the status echo: the pre-reset read reports the
	// environment as it was adopted, and once the controller adopts the reset
	// the read reports the new generation even while the wipe is in flight.
	recorder = playgroundRequest(handler, http.MethodGet, "", true)
	if generation := playgroundBody(t, recorder).Generation; generation == nil || *generation != 0 {
		t.Fatalf("pre-reset generation = %v, want 0", generation)
	}
	state.mu.Lock()
	environment := state.environments[name]
	environment.Status.Operation = runtimev2.OperationResetting
	environment.Status.ObservedResetNonce = nonce
	state.environments[name] = environment
	state.mu.Unlock()
	recorder = playgroundRequest(handler, http.MethodGet, "", true)
	if generation := playgroundBody(t, recorder).Generation; generation == nil || *generation != nonce {
		t.Fatalf("adopted reset generation = %v, want %d", generation, nonce)
	}

	// A failed session is no longer visible as a session: findPlaygroundEnvironment
	// rejects non-live phases, so the read reports none and the next create
	// clears and rebuilds the environment from scratch.
	state.setPlaygroundPhase(t, name, runtimev2.PhaseFailed)
	recorder = playgroundRequest(handler, http.MethodGet, "", true)
	if playgroundBody(t, recorder).State != api.PlaygroundEnvironmentStateNone {
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
