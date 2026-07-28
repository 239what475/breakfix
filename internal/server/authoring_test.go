package server

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/agentruntime"
	"github.com/breakfix/breakfix/internal/api"
	"github.com/breakfix/breakfix/internal/authoring"
	"github.com/breakfix/breakfix/internal/challenge"
	"github.com/breakfix/breakfix/internal/config"
	"github.com/breakfix/breakfix/internal/db"
	"github.com/breakfix/breakfix/internal/generator"
	"github.com/breakfix/breakfix/internal/k8s"
	breakfixv1 "github.com/breakfix/breakfix/internal/k8s/apis/breakfix/v1"
	"github.com/breakfix/breakfix/internal/taxonomy"
	"github.com/breakfix/breakfix/internal/testpostgres"
	"github.com/gin-gonic/gin"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestAuthoringAPIOnlyShowsVerifiedRevision(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	root := t.TempDir()
	database := testpostgres.New(t)
	if _, err := database.CreateUserWithAuth("u-author", "author", "hash", "totp"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateAuthoringSession(ctx, authoring.Session{ID: "author-visible", UserID: "u-author"}, authoring.Plan{}); err != nil {
		t.Fatal(err)
	}
	intent := testAuthoringPlan("Intent title", "intent overview")
	first, err := database.ReplaceAuthoringPlan(ctx, "author-visible", "u-author", 0, intent, authoring.StateIntentReview)
	if err != nil {
		t.Fatal(err)
	}
	firstRun, err := startTestGeneratorRun(ctx, database, "author-visible", "u-author", first.Number, "generator-one")
	if err != nil {
		t.Fatal(err)
	}
	artifactDir := authoring.ArtifactDirectory(root, "author-visible", first.Number)
	writeAuthoringArtifact(t, artifactDir, "Actual verified title", "actual verified description")
	if err := completeTestGeneratorVerification(ctx, database, "author-visible", firstRun, authoring.Artifact{
		SubmissionID: generator.SubmissionID(firstRun.ID), Directory: authoring.ArtifactRelativePath("author-visible", first.Number), GeneratorRunID: firstRun.ID,
	}, "verify-one"); err != nil {
		t.Fatal(err)
	}

	handler := NewHandler(database, nil, config.Config{DataDir: root})
	response := getAuthoringSessionResponse(t, handler, "author-visible")
	if int64(response.VisibleRevision) != first.Number || int64(response.IntentRevision) != first.Number {
		t.Fatalf("unexpected verified revisions: %#v", response)
	}
	if response.Verified == nil || response.Verified.Metadata.Title != "Actual verified title" || response.Intent.Metadata.Title != "Intent title" {
		t.Fatalf("API did not separate intent from verified artifact: %#v", response)
	}
	if len(response.Assets) == 0 || response.Artifact == nil {
		t.Fatalf("verified artifact was not exposed: %#v", response)
	}
	if response.AuthoringTurnActive {
		t.Fatalf("completed authoring session reported an active authoring turn: %#v", response)
	}

	second, err := database.ReplaceAuthoringPlan(ctx, "author-visible", "u-author", first.Number, testAuthoringPlan("Unverified revision", "new intent"), authoring.StateRevisingAndVerifying)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := startTestGeneratorRun(ctx, database, "author-visible", "u-author", second.Number, "generator-two"); err != nil {
		t.Fatal(err)
	}
	response = getAuthoringSessionResponse(t, handler, "author-visible")
	if int64(response.IntentRevision) != second.Number || int64(response.VisibleRevision) != first.Number {
		t.Fatalf("revision boundaries leaked: %#v", response)
	}
	if response.Verified == nil || response.Verified.Metadata.Title != "Actual verified title" {
		t.Fatalf("unverified revision replaced visible artifact: %#v", response.Verified)
	}
	if response.Intent.Metadata.Title != "Intent title" {
		t.Fatalf("unverified intent leaked into visible content: %#v", response.Intent)
	}
}

func TestAuthoringAPIDisablesActionsWhileAuthoringTurnIsActive(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	database := testpostgres.New(t)
	if _, err := database.CreateUserWithAuth("u-active-author", "active-author", "hash", "totp"); err != nil {
		t.Fatal(err)
	}
	session, err := database.CreateAuthoringSession(ctx, authoring.Session{ID: "author-active-turn", UserID: "u-active-author"}, authoring.Plan{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := database.StartAuthoringRun(ctx, session.ID, session.UserID, agentruntime.Message{
		ID: "active-author-message", Role: "user", Content: "创建一题容器排障题",
	}, agentruntime.CreateRun{
		ID: "active-author-run", SessionID: session.RuntimeSessionID, Purpose: "authoring", OwnerKind: "authoring-session", OwnerRef: session.ID,
		Input: json.RawMessage(`{"base_revision":0}`), Model: "test-model", PromptVersion: "authoring-v1", DeadlineAt: time.Now().UTC().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	handler := NewHandler(database, nil, config.Config{DataDir: t.TempDir()})
	response := getAuthoringSessionResponseForUser(t, handler, session.ID, session.UserID)
	if !response.AuthoringTurnActive {
		t.Fatalf("pending authoring run was not exposed to the client: %#v", response)
	}
}

func TestCurrentAuthoringSessionResumesOnlyUnpublishedWork(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	root := t.TempDir()
	database := testpostgres.New(t)
	if _, err := database.CreateUserWithAuth("u-current", "current", "hash", "totp"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateAuthoringSession(ctx, authoring.Session{ID: "older", UserID: "u-current"}, authoring.Plan{}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateAuthoringSession(ctx, authoring.Session{ID: "newer", UserID: "u-current"}, authoring.Plan{}); err != nil {
		t.Fatal(err)
	}

	handler := NewHandler(database, nil, config.Config{DataDir: root})
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/authoring/sessions/current", nil)
	c.Set("user_id", "u-current")
	handler.GetCurrentAuthoringSession(c)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected current authoring session, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var response api.AuthoringSession
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Id != "newer" {
		t.Fatalf("expected latest session to resume, got %#v", response)
	}
}

func TestAuthoringPublishRecoversAfterFilesystemPromotion(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	database := testpostgres.New(t)
	if _, err := database.CreateUserWithAuth("u-publish", "publish", "hash", "totp"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateAuthoringSession(ctx, authoring.Session{ID: "author-publish", UserID: "u-publish"}, authoring.Plan{}); err != nil {
		t.Fatal(err)
	}
	revision, err := database.ReplaceAuthoringPlan(ctx, "author-publish", "u-publish", 0, testAuthoringPlan("Publish title", "publish overview"), authoring.StateIntentReview)
	if err != nil {
		t.Fatal(err)
	}
	run, err := startTestGeneratorRun(ctx, database, "author-publish", "u-publish", revision.Number, "generator-publish")
	if err != nil {
		t.Fatal(err)
	}
	artifactDir := authoring.ArtifactDirectory(root, "author-publish", revision.Number)
	writeAuthoringArtifact(t, artifactDir, "Verified publish title", "verified publish description")
	artifact := authoring.Artifact{
		SubmissionID:   generator.SubmissionID(run.ID),
		Directory:      authoring.ArtifactRelativePath("author-publish", revision.Number),
		GeneratorRunID: run.ID,
	}
	if err := completeTestGeneratorVerification(ctx, database, "author-publish", run, artifact, "vt-publish"); err != nil {
		t.Fatal(err)
	}

	handler := NewHandler(database, nil, config.Config{DataDir: root})
	const challengeID = "chal-publish-recovery"
	if _, err := database.BeginPublish(ctx, "author-publish", "u-publish", revision.Number, challengeID); err != nil {
		t.Fatal(err)
	}
	// This is the exact crash window: the catalog rename finished before the
	// database could record the published session state.
	if _, err := challenge.PromoteDirectory(handler.challengesDir, artifactDir, challengeID, "registry.example/verify:latest"); err != nil {
		t.Fatal(err)
	}
	if err := handler.syncAuthoringSession(ctx, "author-publish"); err != nil {
		t.Fatal(err)
	}

	session, err := database.GetAuthoringSession(ctx, "author-publish", "u-publish")
	if err != nil {
		t.Fatal(err)
	}
	if session.State != authoring.StatePublished {
		t.Fatalf("filesystem-promoted revision was not recovered: %#v", session)
	}
	stored, err := database.GetAuthoringRevision(ctx, session.ID, revision.Number)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Verification == nil || stored.Verification.ChallengeID != challengeID {
		t.Fatalf("recovered publication did not persist challenge ID: %#v", stored)
	}
	response := getAuthoringSessionResponseForUser(t, handler, session.ID, session.UserID)
	if response.PublishChallengeId == nil || *response.PublishChallengeId != challengeID {
		t.Fatalf("published session did not expose challenge ID: %#v", response)
	}
	published, err := challenge.Get(handler.challengesDir, challengeID)
	if err != nil {
		t.Fatal(err)
	}
	work, err := database.GetTaxonomyWorkByChallenge(ctx, taxonomy.WorkKindMapping, challengeID, published.Revision)
	if err != nil || work.State != taxonomy.WorkPending {
		t.Fatalf("recovered publication did not enqueue taxonomy mapping: %#v, %v", work, err)
	}
}

func TestPromoteVerifiedRevisionImmediatelyEnqueuesTaxonomy(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	database := testpostgres.New(t)
	if _, err := database.CreateUserWithAuth("u-normal-publish", "normal-publish", "hash", "totp"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateAuthoringSession(ctx, authoring.Session{ID: "author-normal-publish", UserID: "u-normal-publish"}, authoring.Plan{}); err != nil {
		t.Fatal(err)
	}
	revision, err := database.ReplaceAuthoringPlan(ctx, "author-normal-publish", "u-normal-publish", 0, testAuthoringPlan("Normal publish", "normal publish overview"), authoring.StateIntentReview)
	if err != nil {
		t.Fatal(err)
	}
	run, err := startTestGeneratorRun(ctx, database, "author-normal-publish", "u-normal-publish", revision.Number, "generator-normal-publish")
	if err != nil {
		t.Fatal(err)
	}
	artifactDir := authoring.ArtifactDirectory(root, "author-normal-publish", revision.Number)
	writeAuthoringArtifact(t, artifactDir, "Normal verified publish", "normal verified description")
	artifact := authoring.Artifact{
		SubmissionID:   generator.SubmissionID(run.ID),
		Directory:      authoring.ArtifactRelativePath("author-normal-publish", revision.Number),
		GeneratorRunID: run.ID,
	}
	const verifyTaskID = "vt-normal-publish"
	if err := completeTestGeneratorVerification(ctx, database, "author-normal-publish", run, artifact, verifyTaskID); err != nil {
		t.Fatal(err)
	}

	const challengeID = "chal-normal-publish"
	registryAddr, verifiedImage, publishedImage := publishingRegistry(t, challengeID)
	kube := verifiedTaskKubernetesClient(t, root, verifyTaskID, verifiedImage)
	handler := NewHandler(database, kube, config.Config{
		DataDir:          root,
		CRDNamespace:     "breakfix-system",
		RegistryAddr:     registryAddr,
		RegistryInsecure: true,
	})
	stored, err := database.BeginPublish(ctx, "author-normal-publish", "u-normal-publish", revision.Number, challengeID)
	if err != nil {
		t.Fatal(err)
	}
	session, err := database.GetAuthoringSession(ctx, "author-normal-publish", "u-normal-publish")
	if err != nil {
		t.Fatal(err)
	}
	user, err := database.GetUserByID("u-normal-publish")
	if err != nil {
		t.Fatal(err)
	}
	if err := handler.promoteVerifiedRevision(ctx, user, session, stored, challengeID); err != nil {
		t.Fatal(err)
	}

	published, err := challenge.Get(handler.challengesDir, challengeID)
	if err != nil {
		t.Fatal(err)
	}
	if published.Image != publishedImage {
		t.Fatalf("published image = %q, want trusted promoted image %q", published.Image, publishedImage)
	}
	work, err := database.GetTaxonomyWorkByChallenge(ctx, taxonomy.WorkKindMapping, challengeID, published.Revision)
	if err != nil || work.State != taxonomy.WorkPending {
		t.Fatalf("normal publication did not enqueue taxonomy mapping: %#v, %v", work, err)
	}
}

func TestStoreVerifiedArtifactIsIdempotentAcrossConcurrentSyncs(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	writeAuthoringArtifact(t, source, "Concurrent verified title", "concurrent verified description")
	archive := archiveAuthoringArtifact(t, source)
	const submissionID = "sub-concurrent-artifact"
	if _, err := challenge.SaveSubmission(root, submissionID, bytes.NewReader(archive)); err != nil {
		t.Fatal(err)
	}

	handler := NewHandler(nil, nil, config.Config{DataDir: root})
	session := &authoring.Session{ID: "author-concurrent", CurrentRevision: 1, GeneratorRunID: "generator-concurrent"}
	task := &breakfixv1.VerifyTask{Spec: breakfixv1.VerifyTaskSpec{Submission: breakfixv1.VerifyTaskSubmission{ID: submissionID}}}

	const callers = 8
	start := make(chan struct{})
	errs := make(chan error, callers)
	artifacts := make(chan authoring.Artifact, callers)
	var callersWG sync.WaitGroup
	for range callers {
		callersWG.Add(1)
		go func() {
			defer callersWG.Done()
			<-start
			artifact, err := handler.storeVerifiedArtifact(context.Background(), session, task)
			if err != nil {
				errs <- err
				return
			}
			artifacts <- artifact
		}()
	}
	close(start)
	callersWG.Wait()
	close(errs)
	close(artifacts)

	for err := range errs {
		t.Fatalf("concurrent verified artifact sync: %v", err)
	}
	for artifact := range artifacts {
		if artifact.SubmissionID != submissionID || artifact.Directory != authoring.ArtifactRelativePath(session.ID, session.CurrentRevision) {
			t.Fatalf("artifact = %#v", artifact)
		}
	}
	if _, err := authoring.ReadVerifiedChallenge(root, &authoring.Artifact{
		SubmissionID: submissionID,
		Directory:    authoring.ArtifactRelativePath(session.ID, session.CurrentRevision),
	}); err != nil {
		t.Fatalf("read concurrent verified artifact: %v", err)
	}
}

func startTestGeneratorRun(ctx context.Context, database *db.DB, sessionID, userID string, revision int64, runID string) (*agentruntime.Run, error) {
	_, run, err := database.StartGeneratorRun(ctx, sessionID, userID, revision, agentruntime.CreateRun{
		ID: runID, Purpose: generator.RuntimePurpose, OwnerKind: "authoring-session", OwnerRef: sessionID,
		Model: "test-model", PromptVersion: generator.PromptVersion, DeadlineAt: time.Now().UTC().Add(time.Hour),
	}, generator.RunInput{AuthoringSessionID: sessionID, Revision: revision})
	return run, err
}

func completeTestGeneratorVerification(ctx context.Context, database *db.DB, sessionID string, run *agentruntime.Run, artifact authoring.Artifact, taskID string) error {
	claim, err := database.ClaimNext(ctx, "test-generator-worker", time.Minute, time.Now().UTC())
	if err != nil {
		return err
	}
	if claim == nil || claim.Run.ID != run.ID {
		return fmt.Errorf("claim generator run %q", run.ID)
	}
	if err := database.FinalizeGeneratorSubmission(ctx, *claim, artifact.SubmissionID, taskID); err != nil {
		return err
	}
	return database.CompleteGeneratorVerification(ctx, sessionID, run.ID, artifact, authoring.Verification{
		TaskID: taskID, Phase: "Succeeded", Report: &authoring.VerificationReport{BuildPassed: true, AnswerPassed: true, CheckpointsPassed: true},
	})
}

func getAuthoringSessionResponse(t *testing.T, handler *Handler, sessionID string) api.AuthoringSession {
	return getAuthoringSessionResponseForUser(t, handler, sessionID, "u-author")
}

func getAuthoringSessionResponseForUser(t *testing.T, handler *Handler, sessionID, userID string) api.AuthoringSession {
	t.Helper()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/authoring/sessions/"+sessionID, nil)
	c.Set("user_id", userID)
	handler.GetAuthoringSession(c, sessionID)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected authoring session, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var response api.AuthoringSession
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response
}

func testAuthoringPlan(title, overview string) authoring.Plan {
	return authoring.Plan{
		Metadata: authoring.Metadata{Title: title, Description: "intent description", Difficulty: "easy", Runtime: "container"},
		Overview: overview,
		Checkpoints: []authoring.Checkpoint{{
			ID: "service-ready", Title: "Service ready", Markdown: "The service is ready.", Position: 1,
		}},
	}
}

func writeAuthoringArtifact(t *testing.T, root, title, description string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "challenge.yaml"), "title: "+title+"\ntype: script\nruntime: container\ndifficulty: medium\ndescription: "+description+"\ncheckpoints:\n  - id: service-ready\n    title: Service ready\n    description: The service responds successfully.\n    hint: hints/service-ready.md\n")
	writeTestFile(t, filepath.Join(root, "Dockerfile"), "FROM breakfix-base:latest\n")
	writeTestFile(t, filepath.Join(root, "generate.sh"), "#!/bin/sh\n")
	writeTestFile(t, filepath.Join(root, "problem.md"), "# Actual problem\n")
	writeTestFile(t, filepath.Join(root, "solution.md"), "# Actual solution\n<!-- checkpoint: service-ready -->\n")
	writeTestFile(t, filepath.Join(root, "hints", "service-ready.md"), "hint\n")
	writeTestFile(t, filepath.Join(root, "checks", "checkpoints.sh"), "#!/bin/sh\n")
	writeTestFile(t, filepath.Join(root, "answer.sh"), "#!/bin/sh\n")
}

func verifiedTaskKubernetesClient(t *testing.T, root, verifyTaskID, image string) *k8s.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		wantPath := "/apis/breakfix.dev/v1/namespaces/breakfix-system/verifytasks/" + verifyTaskID
		if request.Method != http.MethodGet || request.URL.Path != wantPath {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(writer).Encode(breakfixv1.VerifyTask{
			TypeMeta:   metav1.TypeMeta{APIVersion: "breakfix.dev/v1", Kind: "VerifyTask"},
			ObjectMeta: metav1.ObjectMeta{Name: verifyTaskID, Namespace: "breakfix-system"},
			Status:     breakfixv1.VerifyTaskStatus{Phase: breakfixv1.VerifyTaskSucceeded, Image: image},
		}); err != nil {
			t.Errorf("write verify task: %v", err)
		}
	}))
	t.Cleanup(server.Close)
	kubeconfig := filepath.Join(root, "kubeconfig")
	writeTestFile(t, kubeconfig, "apiVersion: v1\nclusters:\n- cluster:\n    server: "+server.URL+"\n  name: test\ncontexts:\n- context:\n    cluster: test\n    user: test\n  name: test\ncurrent-context: test\nkind: Config\nusers:\n- name: test\n  user: {}\n")
	client, err := k8s.New(kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func publishingRegistry(t *testing.T, challengeID string) (registryAddr, verifiedImage, publishedImage string) {
	t.Helper()
	configBlob := []byte(`{"architecture":"amd64","os":"linux"}`)
	layerBlob := []byte("layer-data")
	configDigest := testRegistryDigest(configBlob)
	layerDigest := testRegistryDigest(layerBlob)
	manifest := []byte(fmt.Sprintf(`{"schemaVersion":2,"config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"%s","size":%d},"layers":[{"mediaType":"application/vnd.oci.image.layer.v1.tar","digest":"%s","size":%d}]}`,
		configDigest, len(configBlob), layerDigest, len(layerBlob)))
	manifestDigest := testRegistryDigest(manifest)
	targetPath := "/v2/team/challenge-" + challengeID + "/manifests/latest"
	published := false

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/v2/team/verified/manifests/"+manifestDigest:
			writer.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
			writer.Header().Set("Docker-Content-Digest", manifestDigest)
			_, _ = writer.Write(manifest)
		case request.Method == http.MethodGet && request.URL.Path == "/v2/team/verified/blobs/"+configDigest:
			_, _ = writer.Write(configBlob)
		case request.Method == http.MethodGet && request.URL.Path == "/v2/team/verified/blobs/"+layerDigest:
			_, _ = writer.Write(layerBlob)
		case request.Method == http.MethodHead && strings.HasPrefix(request.URL.Path, "/v2/team/challenge-"+challengeID+"/blobs/"):
			writer.WriteHeader(http.StatusOK)
		case request.Method == http.MethodPut && request.URL.Path == targetPath:
			body, err := io.ReadAll(request.Body)
			if err != nil || !bytes.Equal(body, manifest) {
				writer.WriteHeader(http.StatusBadRequest)
				return
			}
			published = true
			writer.WriteHeader(http.StatusCreated)
		case request.Method == http.MethodHead && request.URL.Path == targetPath:
			if !published {
				writer.WriteHeader(http.StatusNotFound)
				return
			}
			writer.Header().Set("Docker-Content-Digest", manifestDigest)
			writer.WriteHeader(http.StatusOK)
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	address := strings.TrimPrefix(server.URL, "http://")
	registryAddr = address + "/team"
	verifiedImage = registryAddr + "/verified@" + manifestDigest
	publishedImage = registryAddr + "/challenge-" + challengeID + "@" + manifestDigest
	return registryAddr, verifiedImage, publishedImage
}

func testRegistryDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return fmt.Sprintf("sha256:%x", sum[:])
}

func archiveAuthoringArtifact(t *testing.T, root string) []byte {
	t.Helper()

	var archive bytes.Buffer
	gzipWriter := gzip.NewWriter(&archive)
	tarWriter := tar.NewWriter(gzipWriter)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if err := tarWriter.WriteHeader(&tar.Header{Name: filepath.ToSlash(relative), Mode: 0644, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		_, err = tarWriter.Write(content)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return archive.Bytes()
}
