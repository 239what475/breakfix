package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	runtimev2 "github.com/breakfix/breakfix/api/v2"
	"github.com/breakfix/breakfix/internal/adapter/kubernetes"
	"github.com/breakfix/breakfix/internal/adapter/postgres"
	"github.com/breakfix/breakfix/internal/bootstrap/config"
	"github.com/breakfix/breakfix/internal/domain/audit"
	api "github.com/breakfix/breakfix/internal/transport/httpapi/generated"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// environmentAdminFake serves the RuntimeEnvironment collection, item reads,
// and item updates used by the admin environment endpoints.
type environmentAdminFake struct {
	mu           sync.Mutex
	environments map[string]runtimev2.RuntimeEnvironment
	updates      int
}

func (f *environmentAdminFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/apis/breakfix.dev/v2/namespaces/breakfix-system/runtimeenvironments":
		items := make([]runtimev2.RuntimeEnvironment, 0, len(f.environments))
		for _, environment := range f.environments {
			items = append(items, environment)
		}
		_ = json.NewEncoder(w).Encode(runtimev2.RuntimeEnvironmentList{
			TypeMeta: metav1.TypeMeta{APIVersion: "breakfix.dev/v2", Kind: "RuntimeEnvironmentList"},
			Items:    items,
		})
	case r.Method == http.MethodGet || r.Method == http.MethodPut:
		name := filepath.Base(r.URL.Path)
		environment, exists := f.environments[name]
		if !exists {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"kind": "Status", "status": "Failure", "reason": "NotFound", "code": 404})
			return
		}
		if r.Method == http.MethodPut {
			var updated runtimev2.RuntimeEnvironment
			if err := json.NewDecoder(r.Body).Decode(&updated); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			f.environments[name] = updated
			f.updates++
			environment = updated
		}
		environment.TypeMeta = metav1.TypeMeta{APIVersion: "breakfix.dev/v2", Kind: "RuntimeEnvironment"}
		_ = json.NewEncoder(w).Encode(environment)
	default:
		http.NotFound(w, r)
	}
}

const adminEnvironmentRevisionDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func environmentFixture(name string, purpose runtimev2.EnvironmentPurpose, phase runtimev2.EnvironmentPhase) runtimev2.RuntimeEnvironment {
	now := metav1.NewTime(time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC))
	environment := runtimev2.RuntimeEnvironment{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: "breakfix-system", UID: types.UID("uid-" + name), CreationTimestamp: now,
			Labels: map[string]string{
				"breakfix.dev/user":         "u-member",
				"breakfix.dev/content-kind": "operations",
				"breakfix.dev/content-id":   "demo",
			},
		},
		Spec: runtimev2.RuntimeEnvironmentSpec{
			RunnableRevisionRef: runtimev2.RunnableRevisionReference{ID: "revision-01", Digest: adminEnvironmentRevisionDigest},
			Purpose:             purpose,
			Lease:               runtimev2.LeaseSpec{RenewedAt: now},
		},
		Status: runtimev2.RuntimeEnvironmentStatus{Phase: phase, Lifecycle: runtimev2.EnvironmentLifecycleStatus{ExpiresAt: &now}},
	}
	if phase == runtimev2.PhaseFailed {
		environment.Status.Failure = &runtimev2.EnvironmentFailure{
			Class: runtimev2.FailureInfrastructure, Component: "vcluster", Reason: "provision-lost", Message: "control plane vanished",
			At: now,
		}
	}
	return environment
}

func TestAdminEnvironmentListAndReleaseFollowTheDrainPath(t *testing.T) {
	fake := &environmentAdminFake{environments: map[string]runtimev2.RuntimeEnvironment{
		"env-verification": environmentFixture("env-verification", runtimev2.PurposeVerification, runtimev2.PhaseReady),
		"env-failed":       environmentFixture("env-failed", runtimev2.PurposeLearning, runtimev2.PhaseFailed),
		"env-released":     environmentFixture("env-released", runtimev2.PurposeLearning, runtimev2.PhaseReleased),
	}}
	apiServer := httptest.NewServer(fake)
	t.Cleanup(apiServer.Close)

	server := newAuthTestServer(t, func(cfg *config.Config, dependencies *Dependencies, database *postgres.Store) *kubernetes.Client {
		kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
		contents := "apiVersion: v1\nclusters:\n- cluster:\n    server: " + apiServer.URL + "\n  name: test\ncontexts:\n- context:\n    cluster: test\n    user: test\n  name: test\ncurrent-context: test\nkind: Config\nusers:\n- name: test\n  user: {}\n"
		if err := os.WriteFile(kubeconfig, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		client, err := kubernetes.New(kubeconfig)
		if err != nil {
			t.Fatal(err)
		}
		cfg.CRDNamespace = "breakfix-system"
		return client
	})
	adminRegister := server.register(t, "alice", "alice-password")
	adminToken := server.login(t, "alice", "alice-password", adminRegister.TotpSecret)
	memberRegister := server.register(t, "bob", "bob-password")
	memberToken := server.login(t, "bob", "bob-password", memberRegister.TotpSecret)

	recorder := server.do(t, http.MethodGet, "/api/admin/environments", memberToken, nil)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("non-admin environment list = %d, want 403", recorder.Code)
	}

	recorder = server.do(t, http.MethodGet, "/api/admin/environments", adminToken, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("environment list = %d: %s", recorder.Code, recorder.Body.String())
	}
	var list struct {
		Environments []api.AdminEnvironment `json:"environments"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Environments) != 3 {
		t.Fatalf("environment list = %#v", list.Environments)
	}
	byName := map[string]api.AdminEnvironment{}
	for _, environment := range list.Environments {
		byName[environment.Name] = environment
	}
	if got := byName["env-verification"]; got.Purpose != "verification" || got.Phase != "Ready" || got.User == nil || *got.User != "u-member" || got.ExpiresAt == nil {
		t.Fatalf("verification environment = %#v", got)
	}
	if got := byName["env-failed"]; got.Failure == nil || got.Failure.Class != "infrastructure" || got.Failure.Reason != "provision-lost" {
		t.Fatalf("failed environment = %#v", got)
	}

	// Releasing the already released environment conflicts.
	recorder = server.do(t, http.MethodPost, "/api/admin/environments/env-released/release", adminToken, nil)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("release of released environment = %d, want 409: %s", recorder.Code, recorder.Body.String())
	}
	recorder = server.do(t, http.MethodPost, "/api/admin/environments/env-missing/release", adminToken, nil)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("release of missing environment = %d, want 404", recorder.Code)
	}
	recorder = server.do(t, http.MethodPost, "/api/admin/environments/env-verification/release", memberToken, nil)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("non-admin release = %d, want 403", recorder.Code)
	}

	// The admin release sets lease.releaseAt; the drain path takes over.
	recorder = server.do(t, http.MethodPost, "/api/admin/environments/env-verification/release", adminToken, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("release = %d: %s", recorder.Code, recorder.Body.String())
	}
	fake.mu.Lock()
	updates := fake.updates
	releaseAt := fake.environments["env-verification"].Spec.Lease.ReleaseAt
	fake.mu.Unlock()
	if updates != 1 || releaseAt == nil {
		t.Fatalf("release updates = %d releaseAt = %#v", updates, releaseAt)
	}
	rows, err := server.db.Audit.ListHumanActions(context.Background(), postgres.HumanActionFilter{Action: audit.ActionEnvironmentRelease, Limit: 5})
	if err != nil || len(rows) != 1 || rows[0].TargetID != "env-verification" {
		t.Fatalf("release audit rows = %#v, %v", rows, err)
	}
	// A repeated release stays idempotent without a second write.
	recorder = server.do(t, http.MethodPost, "/api/admin/environments/env-verification/release", adminToken, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("repeat release = %d", recorder.Code)
	}
	fake.mu.Lock()
	updates = fake.updates
	fake.mu.Unlock()
	if updates != 1 {
		t.Fatalf("repeat release rewrote the environment %d times", updates)
	}
}

func TestAdminSystemEndpointReportsBuildAndServices(t *testing.T) {
	lastTick := time.Date(2026, 9, 17, 12, 30, 0, 0, time.UTC)
	server := newAuthTestServer(t, func(cfg *config.Config, dependencies *Dependencies, database *postgres.Store) *kubernetes.Client {
		dependencies.SystemReport = func(context.Context) (SystemReport, error) {
			return SystemReport{
				Version: "test-version", Commit: "test-commit", BuildTime: "test-build-time",
				CatalogReleaseReference: "registry.example.com/breakfix/catalog@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				Documentation: &SystemDocumentationReport{
					SourceID: "kubernetes", Repository: "https://github.com/kubernetes/website.git",
					Revision: "ce98a43", Version: "snapshot-ce98a43", Language: "en",
				},
				Services: []BackgroundServiceStatus{
					{Name: "learning cleanup", StartedAt: lastTick.Add(-time.Hour), LastTickAt: &lastTick},
					{Name: "interactive agent recovery", StartedAt: lastTick.Add(-time.Hour)},
				},
			}, nil
		}
		return nil
	})
	adminRegister := server.register(t, "alice", "alice-password")
	adminToken := server.login(t, "alice", "alice-password", adminRegister.TotpSecret)
	memberRegister := server.register(t, "bob", "bob-password")
	memberToken := server.login(t, "bob", "bob-password", memberRegister.TotpSecret)

	recorder := server.do(t, http.MethodGet, "/api/admin/system", memberToken, nil)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("non-admin system = %d, want 403", recorder.Code)
	}
	recorder = server.do(t, http.MethodGet, "/api/admin/system", adminToken, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("system = %d: %s", recorder.Code, recorder.Body.String())
	}
	var status api.AdminSystemStatus
	if err := json.Unmarshal(recorder.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.Version != "test-version" || status.Commit != "test-commit" || status.CatalogReleaseReference == nil {
		t.Fatalf("system build section = %#v", status)
	}
	if status.CatalogIntegrity.State != "ok" {
		t.Fatalf("catalog integrity = %#v", status.CatalogIntegrity)
	}
	if status.Documentation == nil || status.Documentation.SourceId != "kubernetes" {
		t.Fatalf("documentation section = %#v", status.Documentation)
	}
	if len(status.Services) != 2 {
		t.Fatalf("services = %#v", status.Services)
	}
	if status.Services[0].Name != "learning cleanup" || status.Services[0].LastTickAt == nil {
		t.Fatalf("ticking service = %#v", status.Services[0])
	}
	if status.Services[1].Name != "interactive agent recovery" || status.Services[1].LastTickAt != nil {
		t.Fatalf("service without ticks = %#v", status.Services[1])
	}
}
