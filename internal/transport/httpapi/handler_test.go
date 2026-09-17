package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/breakfix/breakfix/internal/adapter/kubernetes"
	"github.com/breakfix/breakfix/internal/adapter/postgres"
	appcatalog "github.com/breakfix/breakfix/internal/application/catalog"
	appoperations "github.com/breakfix/breakfix/internal/application/operations"
	"github.com/breakfix/breakfix/internal/bootstrap/config"
	"github.com/breakfix/breakfix/internal/content/scenario"
	"github.com/breakfix/breakfix/internal/domain/audit"
	documentdomain "github.com/breakfix/breakfix/internal/domain/documentpractice"
	"github.com/breakfix/breakfix/internal/domain/runnable"
	scenariodomain "github.com/breakfix/breakfix/internal/domain/scenario"
	"github.com/gin-gonic/gin"
)

func TestStartDocumentationPracticeUsesOnlyTheFixedApplicationPort(t *testing.T) {
	application := &testDocumentationApplication{}
	h := &Handler{documentation: application}
	recorder := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(recorder)
	ginContext.Request = httptest.NewRequest(http.MethodPost, "/api/documentation/practice", nil)
	h.StartDocumentationPractice(ginContext)
	if recorder.Code != http.StatusAccepted || application.calls != 1 {
		t.Fatalf("documentation start = %d, calls = %d", recorder.Code, application.calls)
	}
}

type testDocumentationApplication struct {
	calls  int
	actors []string
}

func (a *testDocumentationApplication) StartDocumentationPractice(_ context.Context, actorID string) (documentdomain.Workflow, error) {
	a.calls++
	a.actors = append(a.actors, actorID)
	return documentdomain.Workflow{ID: "document-workflow-01", State: documentdomain.Planning}, nil
}

func (a *testDocumentationApplication) ForceFailDocumentationWorkflow(context.Context, string, string, *audit.HumanAction) (documentdomain.Workflow, error) {
	return documentdomain.Workflow{}, errors.New("not implemented")
}

func (a *testDocumentationApplication) RestartDocumentationWorkflow(context.Context, string, string, *audit.HumanAction) (documentdomain.Workflow, error) {
	return documentdomain.Workflow{}, errors.New("not implemented")
}

func newHandlerForTest(t testing.TB, database *postgres.Store, client *kubernetes.Client, cfg config.Config) *Handler {
	t.Helper()
	dependencies := Dependencies{}
	if cfg.DataDir != "" {
		lifecycle, resolver, err := testCatalogLifecycle(cfg.ScenariosDir())
		if err != nil {
			t.Fatal(err)
		}
		dependencies.Catalog = appcatalog.NewService(cfg.ScenariosDir(), nil, lifecycle, resolver)
	}
	handler, err := NewHandlerWithDependencies(database, client, cfg, dependencies)
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func testCatalogLifecycle(scenariosDir string) (*testLifecycleStore, *testRunnableResolver, error) {
	entries, err := scenario.List(scenariosDir)
	if err != nil || len(entries) == 0 {
		if err == nil {
			err = fmt.Errorf("test catalog has no materialized scenarios")
		}
		return nil, nil, err
	}
	store := &testLifecycleStore{byID: make(map[string]scenariodomain.ActiveRevision, len(entries))}
	resolver := &testRunnableResolver{revisions: make(map[string]runnable.RunnableRevision, len(entries))}
	for _, entry := range entries {
		if entry.SourceSlug == "" {
			return nil, nil, fmt.Errorf("test scenario %q has no source slug", entry.ID)
		}
		stable := scenariodomain.Scenario{
			ID: entry.ID, SourceKind: scenariodomain.SourceRelease, SourceRef: "test/" + entry.SourceSlug, State: scenariodomain.StateActive,
			ActiveRevisionID: entry.RevisionID, SourceSlug: entry.SourceSlug, CreatedAt: entry.PublishedAt, UpdatedAt: entry.PublishedAt,
		}
		publicRevision, publicReference, err := handlerTestRunnableRevision(entry)
		if err != nil {
			return nil, nil, err
		}
		resolver.revisions[publicReference.ID+":"+publicReference.Digest] = publicRevision
		revision := scenariodomain.Revision{
			ID: entry.RevisionID, ScenarioID: entry.ID, SourceKind: stable.SourceKind, SourceRef: stable.SourceRef, SourceRevisionID: entry.ContentRevision,
			Title: entry.Title, Type: entry.Type, Tags: entry.Tags, ContentRevision: entry.ContentRevision,
			SourceSlug: entry.SourceSlug, MaterializedPath: scenario.MaterializedPath(entry.SourceSlug, entry.RevisionID), MaterializedRevision: entry.Revision,
			RunnableRevisionRef: publicReference, VerificationReportRef: runnable.VerificationReportReference{ID: "verification-report-" + entry.ID, Digest: handlerTestDigest("f")},
			State: scenariodomain.RevisionActive, PublishedAt: entry.PublishedAt, CreatedAt: entry.PublishedAt,
		}
		value := scenariodomain.ActiveRevision{Scenario: stable, Revision: revision}
		if !value.Valid() {
			return nil, nil, fmt.Errorf("test active catalog entry %q is invalid", entry.ID)
		}
		store.active = append(store.active, value)
		store.byID[entry.ID] = value
	}
	return store, resolver, nil
}

type testRunnableResolver struct {
	revisions map[string]runnable.RunnableRevision
}

func (r *testRunnableResolver) ResolveRunnableRevision(_ context.Context, id, digest string) (runnable.RunnableRevision, error) {
	value, found := r.revisions[id+":"+digest]
	if !found {
		return runnable.RunnableRevision{}, fmt.Errorf("test runnable revision %q not found", id)
	}
	return value, nil
}

func handlerTestRunnableRevision(entry scenario.Entry) (runnable.RunnableRevision, runnable.RevisionReference, error) {
	profile := appoperations.RuntimeProfileConfig{ProfileRevision: "test-profile", BaseImage: "registry.example/base@" + handlerTestDigest("a"), Resources: runnable.ResourceLimits{CPU: "2", MemoryBytes: 2 << 30, EphemeralBytes: 4 << 30, MaxProcesses: 256, MaxConcurrentTasks: 2}, Network: runnable.NetworkPrivate, MaxActionTimeout: 1200}
	spec, err := appoperations.Compile(appoperations.Input{ContentID: entry.ID, ContentRevision: entry.ContentRevision, Entry: entry, Source: runnable.SourceArchive{FormatVersion: runnable.FormatVersion, Reference: "runnable-sources/test.tar.gz", Digest: handlerTestDigest("b")}}, appoperations.Config{MaxNodes: 4, Node: profile, K8s: profile, Lifecycle: runnable.LifecyclePolicy{CreateTimeoutSeconds: 600, ResetTimeoutSeconds: 600, StopTimeoutSeconds: 300, ReapTimeoutSeconds: 300, IdleTTLSeconds: 1800, MaxLifetimeSeconds: 3600}})
	if err != nil {
		return runnable.RunnableRevision{}, runnable.RevisionReference{}, err
	}
	specDigest, err := spec.Digest()
	if err != nil {
		return runnable.RunnableRevision{}, runnable.RevisionReference{}, err
	}
	artifactDigest := handlerTestDigest("c")
	providerReference := "incus://test/image@" + artifactDigest
	runtime := runnable.RuntimeNode
	if entry.Runtime == scenario.RuntimeK8s {
		runtime = runnable.RuntimeK8s
		providerReference = entry.Image
		_, artifactDigest, _ = strings.Cut(entry.Image, "@")
	}
	value := runnable.RunnableRevision{FormatVersion: runnable.FormatVersion, Spec: spec, Artifact: runnable.ArtifactReference{FormatVersion: runnable.FormatVersion, Runtime: runtime, ProviderReference: providerReference, ArtifactDigest: artifactDigest, BuiltFromSpecDigest: specDigest, BuilderVersion: "builder-test"}}
	digest, err := value.Digest()
	if err != nil {
		return runnable.RunnableRevision{}, runnable.RevisionReference{}, err
	}
	return value, runnable.RevisionReference{ID: "runnable-revision-" + entry.ID, Digest: digest}, nil
}

func handlerTestDigest(character string) string { return "sha256:" + strings.Repeat(character, 64) }

type testLifecycleStore struct {
	active []scenariodomain.ActiveRevision
	byID   map[string]scenariodomain.ActiveRevision
}

func (s *testLifecycleStore) ListActiveScenarioRevisions(context.Context) ([]scenariodomain.ActiveRevision, error) {
	return append([]scenariodomain.ActiveRevision(nil), s.active...), nil
}

func (s *testLifecycleStore) GetScenario(_ context.Context, id string) (*scenariodomain.Scenario, error) {
	value, exists := s.byID[id]
	if !exists {
		return nil, scenariodomain.ErrNotFound
	}
	stable := value.Scenario
	return &stable, nil
}

func (s *testLifecycleStore) GetScenarioRevision(_ context.Context, id, revisionID string) (*scenariodomain.Revision, error) {
	value, exists := s.byID[id]
	if !exists || value.Revision.ID != revisionID {
		return nil, scenariodomain.ErrNotFound
	}
	revision := value.Revision
	return &revision, nil
}
