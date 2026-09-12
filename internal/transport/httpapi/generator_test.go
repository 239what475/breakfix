package httpapi

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	appgeneration "github.com/breakfix/breakfix/internal/application/generation"
	"github.com/breakfix/breakfix/internal/bootstrap/config"
	"github.com/breakfix/breakfix/internal/content/candidate"
	"github.com/breakfix/breakfix/internal/content/scenario"
	"github.com/breakfix/breakfix/internal/domain/authoring"
	"github.com/breakfix/breakfix/internal/domain/generation"
	"github.com/breakfix/breakfix/internal/domain/toolresult"
	testpostgres "github.com/breakfix/breakfix/internal/testkit/postgres"
	api "github.com/breakfix/breakfix/internal/transport/httpapi/generated"
	"github.com/breakfix/breakfix/internal/transport/httpapi/middleware"
)

func TestGeneratorHTTPAPIForwardsAuthenticatedPlanConfirmation(t *testing.T) {
	service := newGeneratorHTTPService()
	router, cfg := newGeneratorHTTPRouter(t, service)
	plan := generatorHTTPPlan()

	first := generatorHTTPRequest(t, router, cfg, http.MethodPost, "/api/generator/plans", api.GeneratorPlanRequest{
		ExpectedRevision: 0, IdempotencyKey: "plan-request-one", Plan: toAPIAuthoringPlan(plan),
	})
	if first.Code != http.StatusOK {
		t.Fatalf("persist plan status = %d: %s", first.Code, first.Body.String())
	}
	var persisted api.GeneratorPlanResponse
	decodeGeneratorHTTPResponse(t, first, &persisted)
	if persisted.SessionId != "session-one" || persisted.PlanRevision != 1 {
		t.Fatalf("persisted plan = %#v", persisted)
	}
	if service.planUserID != "user-one" || service.planSessionID != "" || service.planExpectedRevision != 0 ||
		service.planIdempotencyKey != "plan-request-one" || service.plan.Metadata.Title != plan.Metadata.Title {
		t.Fatalf("plan forwarding = %#v", service)
	}

	repeated := generatorHTTPRequest(t, router, cfg, http.MethodPost, "/api/generator/plans", api.GeneratorPlanRequest{
		ExpectedRevision: 0, IdempotencyKey: "plan-request-one", Plan: toAPIAuthoringPlan(plan),
	})
	if repeated.Code != http.StatusOK {
		t.Fatalf("repeat plan status = %d: %s", repeated.Code, repeated.Body.String())
	}
	var repeatedPlan api.GeneratorPlanResponse
	decodeGeneratorHTTPResponse(t, repeated, &repeatedPlan)
	if repeatedPlan.SessionId != persisted.SessionId || repeatedPlan.PlanRevision != persisted.PlanRevision ||
		repeatedPlan.Plan.Metadata != persisted.Plan.Metadata || repeatedPlan.Plan.Overview != persisted.Plan.Overview || service.planCalls != 2 {
		t.Fatalf("idempotent plan response = %#v calls=%d", repeatedPlan, service.planCalls)
	}

	confirmation := generatorHTTPRequest(t, router, cfg, http.MethodPost, "/api/generator/workflows", api.GeneratorGenerationConfirmationRequest{
		SessionId: persisted.SessionId, PlanRevision: persisted.PlanRevision, IdempotencyKey: "confirm-request-one",
	})
	if confirmation.Code != http.StatusOK {
		t.Fatalf("confirm generation status = %d: %s", confirmation.Code, confirmation.Body.String())
	}
	var workflow api.GeneratorWorkflow
	decodeGeneratorHTTPResponse(t, confirmation, &workflow)
	if workflow.Id != "workflow-one" || workflow.SessionId != persisted.SessionId || workflow.PlanRevision != persisted.PlanRevision {
		t.Fatalf("confirmed workflow = %#v", workflow)
	}
	if service.confirmUserID != "user-one" || service.confirmSessionID != persisted.SessionId ||
		service.confirmation.PlanRevision != persisted.PlanRevision || service.confirmation.IdempotencyKey != "confirm-request-one" {
		t.Fatalf("confirmation forwarding = %#v", service)
	}
}

func TestGeneratorHTTPAPIUsesExplicitWorkflowTurnBinding(t *testing.T) {
	service := newGeneratorHTTPService()
	router, cfg := newGeneratorHTTPRouter(t, service)

	started := generatorHTTPRequest(t, router, cfg, http.MethodPost, "/api/generator/workflows/workflow-one/workspace/turn", api.GeneratorWorkspaceTurnRequest{TurnId: "turn-one"})
	if started.Code != http.StatusOK {
		t.Fatalf("start turn status = %d: %s", started.Code, started.Body.String())
	}
	if service.startTurnUserID != "user-one" || service.startedTurn != (generation.WorkspaceTurn{WorkflowID: "workflow-one", ID: "turn-one"}) {
		t.Fatalf("start turn forwarding = %#v", service)
	}

	files := generatorHTTPRequest(t, router, cfg, http.MethodGet, "/api/generator/workflows/workflow-one/workspace/files?turn_id=turn-one", nil)
	if files.Code != http.StatusOK {
		t.Fatalf("list workspace files status = %d: %s", files.Code, files.Body.String())
	}
	if service.listFilesUserID != "user-one" || service.listFilesTurn != service.startedTurn {
		t.Fatalf("list files turn forwarding = %#v", service)
	}

	command := generatorHTTPRequest(t, router, cfg, http.MethodPost, "/api/generator/workflows/workflow-one/workspace/commands", api.GeneratorWorkspaceCommandRequest{
		TurnId: "turn-one", Command: "find . -maxdepth 1",
	})
	if command.Code != http.StatusOK {
		t.Fatalf("workspace command status = %d: %s", command.Code, command.Body.String())
	}
	if service.commandUserID != "user-one" || service.commandTurn != service.startedTurn || service.command != "find . -maxdepth 1" {
		t.Fatalf("command forwarding = %#v", service)
	}
}

func TestGeneratorHTTPAPIProjectsSafeGenerationReview(t *testing.T) {
	service := newGeneratorHTTPService()
	archive := generatorHTTPReviewArchive(t)
	archivePath := filepath.Join(t.TempDir(), "private", "candidate.tar.gz")
	if err := os.MkdirAll(filepath.Dir(archivePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archivePath, archive, 0o600); err != nil {
		t.Fatal(err)
	}
	service.generation = &appgeneration.GenerationView{
		Workflow: generatorHTTPWorkflow("workflow-review", "session-one", 2),
		Candidate: &generation.Revision{
			ID: "candidate-review", Source: generation.Source{Kind: generation.SourceAuthoring, Ref: "session-one"}, SourceRevision: "2",
			ArchivePath: archivePath, ArchiveSHA256: candidate.Digest(archive),
			Failure: &generation.Failure{Class: generation.FailureArtifact, Code: "JUDGE_REJECT", Summary: "the candidate does not satisfy the authoring policy"},
		},
	}
	router, cfg := newGeneratorHTTPRouter(t, service)

	response := generatorHTTPRequest(t, router, cfg, http.MethodGet, "/api/generator/workflows/workflow-review", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("get generation status = %d: %s", response.Code, response.Body.String())
	}
	if service.getUserID != "user-one" || service.getWorkflowID != "workflow-review" {
		t.Fatalf("generation ownership forwarding = %#v", service)
	}
	var review api.GeneratorGeneration
	decodeGeneratorHTTPResponse(t, response, &review)
	if review.Workflow.Id != "workflow-review" || review.Candidate == nil || review.Candidate.Id != "candidate-review" ||
		review.Candidate.Failure == nil || review.Candidate.Failure.Class != api.AuthoringCandidateFailureClassArtifact ||
		review.Candidate.Failure.Code != "JUDGE_REJECT" || review.Candidate.Failure.Summary == "" ||
		review.Verified == nil || review.Verified.Metadata.Title != "HTTP review candidate" || len(review.Assets) == 0 {
		t.Fatalf("review projection = %#v", review)
	}
	for _, sensitive := range []string{"archive_path", "private/candidate.tar.gz", "sandbox_id", "pvc_name"} {
		if strings.Contains(response.Body.String(), sensitive) {
			t.Fatalf("safe review projection leaked %q: %s", sensitive, response.Body.String())
		}
	}
}

func TestGeneratorHTTPAPIExportsImmutableContentReviewBundle(t *testing.T) {
	service := newGeneratorHTTPService()
	archive := generatorHTTPReviewArchive(t)
	archivePath := filepath.Join(t.TempDir(), "private", "candidate.tar.gz")
	if err := os.MkdirAll(filepath.Dir(archivePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archivePath, archive, 0o600); err != nil {
		t.Fatal(err)
	}
	workflow := generatorHTTPWorkflow("workflow-review-bundle", "session-one", 2)
	workflow.State = generation.StateNeedsAuthorReview
	workflow.LastError = "Judge approved this candidate."
	service.generation = &appgeneration.GenerationView{
		Workflow: workflow,
		Candidate: &generation.Revision{
			ID: "candidate-review-bundle", Source: generation.Source{Kind: generation.SourceAuthoring, Ref: "session-one"}, SourceRevision: "2",
			ArchivePath: archivePath, ArchiveSHA256: candidate.Digest(archive),
		},
	}
	router, cfg := newGeneratorHTTPRouter(t, service)

	response := generatorHTTPRequest(t, router, cfg, http.MethodGet, "/api/generator/workflows/workflow-review-bundle/review-bundle?kind=content", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("get review bundle status = %d: %s", response.Code, response.Body.String())
	}
	var bundle api.GeneratorReviewBundle
	decodeGeneratorHTTPResponse(t, response, &bundle)
	if bundle.Manifest.SchemaVersion != reviewBundleSchemaVersion || bundle.Manifest.Kind != api.GeneratorReviewManifestKindContent ||
		bundle.Manifest.WorkflowId != workflow.ID || bundle.Manifest.CandidateRevisionId != service.generation.Candidate.ID ||
		bundle.Manifest.WorkflowState != string(generation.StateNeedsAuthorReview) ||
		bundle.Manifest.CandidateArchiveSha256 != service.generation.Candidate.ArchiveSHA256 || bundle.Manifest.PayloadSha256 == "" {
		t.Fatalf("review bundle manifest = %#v", bundle.Manifest)
	}
	payload, err := base64.StdEncoding.DecodeString(bundle.Payload)
	if err != nil {
		t.Fatalf("decode review payload: %v", err)
	}
	if bundle.Manifest.PayloadSha256 != candidate.Digest(payload) {
		t.Fatalf("review payload digest = %s, manifest = %s", candidate.Digest(payload), bundle.Manifest.PayloadSha256)
	}
	entries := generatorHTTPReviewBundleEntries(t, payload)
	for _, required := range []string{
		"overview.md", "judge.md", "verification.md", "checkpoints/ready.md", "candidate/problem.md", "candidate/nodes/host/checks.sh",
	} {
		if _, found := entries[required]; !found {
			t.Fatalf("review bundle is missing %q: %#v", required, entries)
		}
	}
	if _, found := entries["manifest.json"]; found {
		t.Fatalf("review payload must not contain manifest.json: %#v", entries)
	}
	joined := response.Body.String() + "\n" + string(payload)
	for _, sensitive := range []string{"archive_path", archivePath, "sandbox_id", "pvc_name", "generator-http-jwt-secret"} {
		if strings.Contains(joined, sensitive) {
			t.Fatalf("review bundle leaked %q", sensitive)
		}
	}
}

func generatorHTTPReviewBundleEntries(t *testing.T, payload []byte) map[string]string {
	t.Helper()
	gzipReader, err := gzip.NewReader(bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("open review gzip: %v", err)
	}
	defer func() { _ = gzipReader.Close() }()
	tarReader := tar.NewReader(gzipReader)
	entries := map[string]string{}
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			return entries
		}
		if err != nil {
			t.Fatalf("read review entry: %v", err)
		}
		data, err := io.ReadAll(tarReader)
		if err != nil {
			t.Fatalf("read review entry %s: %v", header.Name, err)
		}
		entries[header.Name] = string(data)
	}
}

func newGeneratorHTTPRouter(t *testing.T, service *generatorHTTPService) (http.Handler, config.Config) {
	t.Helper()
	database := testpostgres.New(t)
	if _, err := database.Identity.CreateUserWithAuth("user-one", "user-one", "", ""); err != nil {
		t.Fatalf("create generator API user: %v", err)
	}
	cfg := config.Config{DataDir: t.TempDir(), JWTSecret: "generator-http-jwt-secret"}
	handler, err := NewHandlerWithDependencies(database, nil, cfg, Dependencies{Generator: service})
	if err != nil {
		t.Fatalf("create generator API handler: %v", err)
	}
	router, err := SetupRouter(handler, cfg, nil)
	if err != nil {
		t.Fatalf("register generator API routes: %v", err)
	}
	return router, cfg
}

func generatorHTTPRequest(t *testing.T, router http.Handler, cfg config.Config, method, path string, value any) *httptest.ResponseRecorder {
	t.Helper()
	var body *bytes.Reader
	if value == nil {
		body = bytes.NewReader(nil)
	} else {
		payload, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("encode generator request: %v", err)
		}
		body = bytes.NewReader(payload)
	}
	request := httptest.NewRequest(method, path, body)
	if value != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	token, err := middleware.GenerateJWT("user-one", "User One", []byte(cfg.JWTSecret))
	if err != nil {
		t.Fatalf("create generator API token: %v", err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

func decodeGeneratorHTTPResponse(t *testing.T, recorder *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.Unmarshal(recorder.Body.Bytes(), target); err != nil {
		t.Fatalf("decode generator response: %v: %s", err, recorder.Body.String())
	}
}

func generatorHTTPPlan() authoring.Plan {
	return authoring.Plan{
		Metadata:    authoring.Metadata{Title: "HTTP Generator", Description: "Exercise the generator HTTP application contract.", Runtime: scenario.RuntimeNode},
		Overview:    "Build a small node workspace through the generator API.",
		Checkpoints: []authoring.Checkpoint{{ID: "ready", Title: "Ready", Markdown: "The node is ready.", Position: 1}},
	}
}

func generatorHTTPWorkflow(id, sessionID string, planRevision int64) generation.Workflow {
	now := time.Date(2026, time.August, 13, 12, 0, 0, 0, time.UTC)
	return generation.Workflow{
		ID: id, Source: generation.Source{Kind: generation.SourceAuthoring, Ref: sessionID}, SourceRevision: strconv.FormatInt(planRevision, 10),
		State: generation.StateGenerating, StateVersion: 1, NextRunAt: now, CreatedAt: now, UpdatedAt: now,
	}
}

func generatorHTTPReviewArchive(t *testing.T) []byte {
	t.Helper()
	var data bytes.Buffer
	gzipWriter := gzip.NewWriter(&data)
	tarWriter := tar.NewWriter(gzipWriter)
	for _, file := range []struct {
		name    string
		content string
		mode    int64
	}{
		{"scenario.yaml", "runtime: node\ntype: operations-scenario\ntitle: HTTP review candidate\ndescription: Review the public candidate projection.\nnodes:\n  - name: host\n    title: Host\ncheckpoints:\n  - id: ready\n    title: Ready\n    description: The workspace is ready.\n    hint: hints/ready.md\n    node: host\n", 0o644},
		{"problem.md", "# Problem\n\nInspect the candidate.\n", 0o644},
		{"solution.md", "# Solution\n\n<!-- checkpoint: ready -->\n", 0o644},
		{"hints/ready.md", "# Hint\n\nInspect the host.\n", 0o644},
		{"nodes/host/generate.sh", "#!/bin/sh\nexit 0\n", 0o755},
		{"nodes/host/answer.sh", "#!/bin/sh\nexit 0\n", 0o755},
		{"nodes/host/checks.sh", "#!/bin/sh\nexit 0\n", 0o755},
	} {
		header := &tar.Header{Name: file.name, Mode: file.mode, Size: int64(len(file.content)), Typeflag: tar.TypeReg}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatalf("write review archive header %s: %v", file.name, err)
		}
		if _, err := tarWriter.Write([]byte(file.content)); err != nil {
			t.Fatalf("write review archive file %s: %v", file.name, err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatalf("close review archive tar: %v", err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatalf("close review archive gzip: %v", err)
	}
	return data.Bytes()
}

type generatorHTTPService struct {
	planUserID           string
	planSessionID        string
	planExpectedRevision int64
	planIdempotencyKey   string
	plan                 authoring.Plan
	planCalls            int

	confirmUserID    string
	confirmSessionID string
	confirmation     generation.StartConfirmation

	getUserID     string
	getWorkflowID string
	generation    *appgeneration.GenerationView

	startTurnUserID string
	startedTurn     generation.WorkspaceTurn
	listFilesUserID string
	listFilesTurn   generation.WorkspaceTurn
	commandUserID   string
	commandTurn     generation.WorkspaceTurn
	command         string
}

func newGeneratorHTTPService() *generatorHTTPService {
	return &generatorHTTPService{generation: &appgeneration.GenerationView{Workflow: generatorHTTPWorkflow("workflow-one", "session-one", 1)}}
}

func (s *generatorHTTPService) SetGenerationPlan(_ context.Context, userID, sessionID string, expectedRevision int64, idempotencyKey string, plan authoring.Plan) (*authoring.Session, *authoring.Revision, error) {
	s.planUserID, s.planSessionID, s.planExpectedRevision, s.planIdempotencyKey, s.plan = userID, sessionID, expectedRevision, idempotencyKey, plan
	s.planCalls++
	return &authoring.Session{ID: "session-one", UserID: userID, CurrentRevision: 1, State: authoring.StateIntentReview}, &authoring.Revision{Number: 1, Plan: plan}, nil
}

func (s *generatorHTTPService) ConfirmGeneration(_ context.Context, userID, sessionID string, confirmation generation.StartConfirmation) (*generation.Workflow, error) {
	s.confirmUserID, s.confirmSessionID, s.confirmation = userID, sessionID, confirmation
	workflow := generatorHTTPWorkflow("workflow-one", sessionID, confirmation.PlanRevision)
	return &workflow, nil
}

func (s *generatorHTTPService) GetGeneration(_ context.Context, userID, workflowID string) (*appgeneration.GenerationView, error) {
	s.getUserID, s.getWorkflowID = userID, workflowID
	value := *s.generation
	return &value, nil
}

func (s *generatorHTTPService) GetGenerationWorkflow(_ context.Context, _ string, workflowID string) (*generation.Workflow, error) {
	workflow := s.generation.Workflow
	workflow.ID = workflowID
	return &workflow, nil
}

func (s *generatorHTTPService) ListActiveGenerations(context.Context, string) ([]generation.Workflow, error) {
	return []generation.Workflow{s.generation.Workflow}, nil
}

func (s *generatorHTTPService) StartWorkspaceTurn(_ context.Context, userID string, turn generation.WorkspaceTurn) error {
	s.startTurnUserID, s.startedTurn = userID, turn
	return nil
}

func (*generatorHTTPService) EndWorkspaceTurn(context.Context, string, generation.WorkspaceTurn) error {
	return nil
}

func (s *generatorHTTPService) ListWorkspaceFiles(_ context.Context, userID string, turn generation.WorkspaceTurn) ([]generation.WorkspaceFile, error) {
	s.listFilesUserID, s.listFilesTurn = userID, turn
	return []generation.WorkspaceFile{{Path: "scenario.yaml", Size: 12}}, nil
}

func (*generatorHTTPService) ReadWorkspaceFile(context.Context, string, generation.WorkspaceTurn, string, int, int) (appgeneration.FileReadResponse, error) {
	return appgeneration.FileReadResponse{Content: "content\n"}, nil
}

func (*generatorHTTPService) WriteWorkspaceFile(context.Context, string, generation.WorkspaceTurn, string, string) error {
	return nil
}

func (s *generatorHTTPService) ExecuteWorkspaceCommand(_ context.Context, userID string, turn generation.WorkspaceTurn, command string) (toolresult.Envelope, error) {
	s.commandUserID, s.commandTurn, s.command = userID, turn, command
	return toolresult.WithData(toolresult.Succeeded, appgeneration.WorkspaceCommand{
		WorkflowID: turn.WorkflowID, ExitCode: 0, Output: "ok\n",
	}, "")
}

func (*generatorHTTPService) SubmitCandidate(context.Context, string, generation.CandidateSubmission) (*generation.Revision, error) {
	return &generation.Revision{ID: "candidate-one"}, nil
}

func (*generatorHTTPService) ConfirmContent(context.Context, string, generation.ContentConfirmation) (*generation.Workflow, error) {
	workflow := generatorHTTPWorkflow("workflow-one", "session-one", 1)
	return &workflow, nil
}

func (*generatorHTTPService) RequestContentChanges(context.Context, string, generation.ContentChangeRequest) (*generation.Workflow, error) {
	workflow := generatorHTTPWorkflow("workflow-one", "session-one", 1)
	return &workflow, nil
}

func (*generatorHTTPService) CancelGeneration(context.Context, string, generation.Cancellation) (*generation.Workflow, error) {
	workflow := generatorHTTPWorkflow("workflow-one", "session-one", 1)
	return &workflow, nil
}

var _ generatorApplication = (*generatorHTTPService)(nil)
