package server

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/agentruntime"
	"github.com/breakfix/breakfix/internal/api"
	"github.com/breakfix/breakfix/internal/authoring"
	"github.com/breakfix/breakfix/internal/candidate"
	"github.com/breakfix/breakfix/internal/challenge"
	"github.com/breakfix/breakfix/internal/config"
	"github.com/breakfix/breakfix/internal/db"
	"github.com/breakfix/breakfix/internal/generator"
	"github.com/breakfix/breakfix/internal/incusprovider"
	"github.com/breakfix/breakfix/internal/taxonomy"
	"github.com/breakfix/breakfix/internal/testpostgres"
	"github.com/breakfix/breakfix/internal/worklist"
	"github.com/gin-gonic/gin"
)

func TestAuthoringAPIOnlyShowsVerifiedCandidate(t *testing.T) {
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
	verifiedCandidate, _ := seedVerifiedAuthoringCandidate(t, database, root, "author-visible", "u-author", first.Number, "generator-one", "Actual verified title")

	handler := NewHandler(database, nil, config.Config{DataDir: root})
	response := getAuthoringSessionResponse(t, handler, "author-visible")
	if int64(response.VisibleRevision) != first.Number || int64(response.IntentRevision) != first.Number {
		t.Fatalf("unexpected verified revisions: %#v", response)
	}
	if response.Verified == nil || response.Verified.Metadata.Title != "Actual verified title" || response.Intent.Metadata.Title != "Intent title" {
		t.Fatalf("API did not separate intent from verified candidate: %#v", response)
	}
	if len(response.Assets) == 0 || response.Candidate == nil || response.Candidate.Id != verifiedCandidate.ID || response.Verification == nil || !response.Verification.Passed {
		t.Fatalf("verified candidate was not exposed: %#v", response)
	}
	if response.AuthoringTurnActive {
		t.Fatalf("completed authoring session reported an active authoring turn: %#v", response)
	}

	second, err := database.ReplaceAuthoringPlan(ctx, "author-visible", "u-author", first.Number, testAuthoringPlan("Unverified revision", "new intent"), authoring.StateRevisingAndVerifying)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := database.StartGeneratorRun(ctx, "author-visible", "u-author", second.Number, generatorRun("generator-two", "author-visible"), generator.RunInput{
		AuthoringSessionID: "author-visible", Revision: second.Number, SeedCandidateRevisionID: verifiedCandidate.ID,
	}); err != nil {
		t.Fatal(err)
	}
	response = getAuthoringSessionResponse(t, handler, "author-visible")
	if int64(response.IntentRevision) != second.Number || int64(response.VisibleRevision) != first.Number {
		t.Fatalf("revision boundaries leaked: %#v", response)
	}
	if response.Candidate == nil || response.Candidate.Id != verifiedCandidate.ID || response.Verified == nil || response.Verified.Metadata.Title != "Actual verified title" {
		t.Fatalf("unverified revision replaced visible candidate: %#v", response)
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
		ID: "active-author-message", Role: "user", Content: "创建一题节点排障题",
	}, agentruntime.CreateRun{
		ID: "active-author-run", SessionID: session.RuntimeSessionID, Purpose: "authoring", OwnerKind: "authoring-session", OwnerRef: session.ID,
		Input: json.RawMessage(`{"base_revision":0}`), Model: "test-model", PromptVersion: "authoring-v1", ExecutionTimeout: time.Hour,
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

	handler := NewHandler(database, nil, config.Config{DataDir: t.TempDir()})
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

func TestAuthoringPublishIsCompletedByFencedPublisherWork(t *testing.T) {
	gin.SetMode(gin.TestMode)
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
	verifiedCandidate, artifact := seedVerifiedAuthoringCandidate(t, database, root, "author-publish", "u-publish", revision.Number, "generator-publish", "Verified publish title")
	handler := NewHandler(database, nil, config.Config{DataDir: root, InternalWorkers: testInternalWorkerKeys(), Incus: incusprovider.Config{NamePrefix: "bf"}})

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/authoring/sessions/author-publish/publish", nil)
	c.Set("user_id", "u-publish")
	handler.PublishAuthoringRevision(c, "author-publish")
	if recorder.Code != http.StatusOK {
		t.Fatalf("publish confirmation = %d: %s", recorder.Code, recorder.Body.String())
	}
	stored, err := database.GetCandidateRevision(ctx, verifiedCandidate.ID)
	if err != nil || stored.State != candidate.StatePublishingChallenge || stored.Publication == nil {
		t.Fatalf("publication intent = %#v, %v", stored, err)
	}
	work, err := database.GetWorkItemForSubject(ctx, worklist.KindChallengePublish, worklist.SubjectCandidateRevision, stored.ID)
	if err != nil || work.State != worklist.StatePending {
		t.Fatalf("challenge publish work = %#v, %v", work, err)
	}
	if _, err := challenge.Get(handler.challengesDir, stored.Publication.ChallengeID); err != challenge.ErrNotFound {
		t.Fatalf("challenge became visible before publisher completion: %v", err)
	}

	claim, err := database.ClaimCandidateWork(ctx, worklist.KindChallengePublish, "publisher-one", time.Minute, time.Now().UTC())
	if err != nil || claim == nil {
		t.Fatalf("claim challenge publication = %#v, %v", claim, err)
	}
	finalArtifact := challengeNodeArtifact(t, stored.Publication.ChallengeID, artifact.IncusFingerprint)
	payload, err := json.Marshal(struct {
		worklist.Credential
		Artifact candidate.ArtifactReference `json:"artifact"`
	}{Credential: claim.Work.Credential(), Artifact: finalArtifact})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/internal/work-items/challenge_publish/"+claim.Work.Item.ID+"/complete/challenge-publish", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Breakfix-Internal-Key", "publisher-test-key")
	response := httptest.NewRecorder()
	router := gin.New()
	router.POST("/api/internal/work-items/:kind/:id/complete/challenge-publish", handler.InternalCompleteCandidateChallengePublish)
	router.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("publisher completion = %d: %s", response.Code, response.Body.String())
	}

	published, err := challenge.Get(handler.challengesDir, stored.Publication.ChallengeID)
	if err != nil {
		t.Fatal(err)
	}
	if published.Image != finalArtifact.IncusFingerprint || published.SourceSlug != stored.Publication.SourceSlug {
		t.Fatalf("published challenge = %#v", published)
	}
	session, err := database.GetAuthoringSession(ctx, "author-publish", "u-publish")
	if err != nil || session.State != authoring.StatePublished || session.PublishChallengeID != published.ID {
		t.Fatalf("published authoring session = %#v, %v", session, err)
	}
	mapping, err := database.GetTaxonomyMappingByChallenge(ctx, published.ID, published.Revision)
	if err != nil || mapping.State != taxonomy.MappingPending {
		t.Fatalf("published challenge taxonomy mapping = %#v, %v", mapping, err)
	}
	cleanup, err := database.GetWorkItemForSubject(ctx, worklist.KindArtifactCleanup, worklist.SubjectCandidateRevision, stored.ID)
	if err != nil || cleanup.State != worklist.StatePending {
		t.Fatalf("published candidate cleanup = %#v, %v", cleanup, err)
	}
}

func TestAuthoringPublishRecoversAfterFinalArtifactWasRecorded(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	root := t.TempDir()
	database := testpostgres.New(t)
	if _, err := database.CreateUserWithAuth("u-recover-publish", "recover-publish", "hash", "totp"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateAuthoringSession(ctx, authoring.Session{ID: "author-recover-publish", UserID: "u-recover-publish"}, authoring.Plan{}); err != nil {
		t.Fatal(err)
	}
	revision, err := database.ReplaceAuthoringPlan(ctx, "author-recover-publish", "u-recover-publish", 0,
		testAuthoringPlan("Recover publication", "recover publication overview"), authoring.StateIntentReview)
	if err != nil {
		t.Fatal(err)
	}
	verified, artifact := seedVerifiedAuthoringCandidate(t, database, root, "author-recover-publish", "u-recover-publish",
		revision.Number, "generator-recover-publish", "Recovered publish title")
	handler := NewHandler(database, nil, config.Config{DataDir: root, Incus: incusprovider.Config{NamePrefix: "bf"}})

	publishRecorder := httptest.NewRecorder()
	publishContext, _ := gin.CreateTestContext(publishRecorder)
	publishContext.Request = httptest.NewRequest(http.MethodPost, "/api/authoring/sessions/author-recover-publish/publish", nil)
	publishContext.Set("user_id", "u-recover-publish")
	handler.PublishAuthoringRevision(publishContext, "author-recover-publish")
	if publishRecorder.Code != http.StatusOK {
		t.Fatalf("publish confirmation = %d: %s", publishRecorder.Code, publishRecorder.Body.String())
	}

	claim, err := database.ClaimCandidateWork(ctx, worklist.KindChallengePublish, "publisher-crashed", time.Minute, time.Now().UTC())
	if err != nil || claim == nil {
		t.Fatalf("claim challenge publication = %#v, %v", claim, err)
	}
	finalArtifact := challengeNodeArtifact(t, mustCandidatePublication(t, database, verified.ID).ChallengeID, artifact.IncusFingerprint)
	if err := database.RecordCandidateChallengeArtifact(ctx, claim.Work, finalArtifact, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	recoverable, err := database.ListRecoverableCandidatePublications(ctx, 10)
	if err != nil || len(recoverable) != 1 || recoverable[0].ID != verified.ID {
		t.Fatalf("recoverable publications = %#v, %v", recoverable, err)
	}
	if recoverable[0].Publication == nil || recoverable[0].Publication.Artifact == nil || *recoverable[0].Publication.Artifact != finalArtifact {
		t.Fatalf("recorded final artifact = %#v", recoverable[0].Publication)
	}
	if _, err := challenge.Get(handler.challengesDir, recoverable[0].Publication.ChallengeID); err != challenge.ErrNotFound {
		t.Fatalf("challenge exists before recovery: %v", err)
	}

	if err := handler.RecoverCandidatePublications(ctx); err != nil {
		t.Fatal(err)
	}
	stored, err := database.GetCandidateRevision(ctx, verified.ID)
	if err != nil || stored.State != candidate.StatePublished || stored.Publication == nil || stored.Publication.Artifact == nil {
		t.Fatalf("recovered candidate = %#v, %v", stored, err)
	}
	published, err := challenge.Get(handler.challengesDir, stored.Publication.ChallengeID)
	if err != nil || published.Image != finalArtifact.IncusFingerprint {
		t.Fatalf("recovered challenge = %#v, %v", published, err)
	}
	work, err := database.GetWorkItemForSubject(ctx, worklist.KindChallengePublish, worklist.SubjectCandidateRevision, verified.ID)
	if err != nil || work.State != worklist.StateSucceeded || work.LeaseOwner != "" {
		t.Fatalf("recovered publication work = %#v, %v", work, err)
	}
	session, err := database.GetAuthoringSession(ctx, "author-recover-publish", "u-recover-publish")
	if err != nil || session.State != authoring.StatePublished {
		t.Fatalf("recovered authoring session = %#v, %v", session, err)
	}
	if _, err := database.GetTaxonomyMappingByChallenge(ctx, published.ID, published.Revision); err != nil {
		t.Fatalf("recovered taxonomy mapping: %v", err)
	}
	cleanup, err := database.GetWorkItemForSubject(ctx, worklist.KindArtifactCleanup, worklist.SubjectCandidateRevision, verified.ID)
	if err != nil || cleanup.State != worklist.StatePending {
		t.Fatalf("recovered cleanup work = %#v, %v", cleanup, err)
	}
}

func seedVerifiedAuthoringCandidate(t *testing.T, database *db.DB, root, sessionID, userID string, revision int64, runID, title string) (*candidate.Revision, candidate.ArtifactReference) {
	t.Helper()
	ctx := context.Background()
	_, run, err := database.StartGeneratorRun(ctx, sessionID, userID, revision, generatorRun(runID, sessionID), generator.RunInput{AuthoringSessionID: sessionID, Revision: revision})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	agentClaim, err := database.ClaimNext(ctx, "generator-"+runID, time.Minute, now)
	if err != nil || agentClaim == nil || agentClaim.Run.ID != run.ID {
		t.Fatalf("claim generator run = %#v, %v", agentClaim, err)
	}
	source := filepath.Join(t.TempDir(), "candidate")
	writeAuthoringCandidate(t, source, title)
	archive := archiveAuthoringCandidate(t, source)
	id := candidate.IDForGeneratorRun(run.ID)
	archivePath, archiveDigest, err := candidate.SaveArchiveAtomic(root, id, archive)
	if err != nil {
		t.Fatal(err)
	}
	revisionCandidate := candidate.Revision{
		ID: id, AuthoringSessionID: sessionID, AuthoringRevision: revision,
		GeneratorSessionID: run.SessionID, GeneratorRunID: run.ID, JudgeRunID: run.ID,
		ArchivePath: archivePath, ArchiveSHA256: archiveDigest, Snapshot: serverNodeSnapshot(), State: candidate.StateBuilding,
	}
	if err := database.FinalizeGeneratorCandidate(ctx, *agentClaim, revisionCandidate, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	buildClaim := claimServerCandidateWork(t, database, worklist.KindBuild)
	build := candidate.BuildOutput{Runtime: challenge.RuntimeNode, Incus: &candidate.IncusBuildReference{
		Project: "breakfix-build", WorkItemID: buildClaim.Work.Item.ID, Attempt: int64(buildClaim.Work.Item.Attempt),
		InstanceName: "build-" + runID, Alias: "build-" + runID, Fingerprint: testFingerprint('b'),
	}}
	if err := database.CompleteCandidateBuild(ctx, buildClaim.Work, build, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	publishClaim := claimServerCandidateWork(t, database, worklist.KindArtifactPublish)
	artifact := candidateNodeArtifact(t, id, testFingerprint('b'))
	if err := database.CompleteCandidateArtifactPublish(ctx, publishClaim.Work, artifact, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	verifyClaim := claimServerCandidateWork(t, database, worklist.KindVerify)
	report := candidate.VerificationReport{
		Passed: true, Summary: "all checks passed",
		Answers:     []candidate.ExecutionResult{{Location: "host", ExitCode: 0}},
		Checkpoints: []candidate.CheckpointResult{{ID: "service-ready", Passed: true, Summary: "service is ready"}},
	}
	if err := database.CompleteCandidateVerification(ctx, verifyClaim.Work, report, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	stored, err := database.GetCandidateRevision(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return stored, artifact
}

func candidateNodeArtifact(t *testing.T, candidateID, fingerprint string) candidate.ArtifactReference {
	t.Helper()
	alias, err := incusprovider.AliasForCandidate("bf", candidateID)
	if err != nil {
		t.Fatal(err)
	}
	return candidate.ArtifactReference{Runtime: challenge.RuntimeNode, IncusAlias: alias, IncusFingerprint: fingerprint}
}

func challengeNodeArtifact(t *testing.T, challengeID, fingerprint string) candidate.ArtifactReference {
	t.Helper()
	alias, err := incusprovider.AliasForChallenge("bf", challengeID)
	if err != nil {
		t.Fatal(err)
	}
	return candidate.ArtifactReference{Runtime: challenge.RuntimeNode, IncusAlias: alias, IncusFingerprint: fingerprint}
}

func mustCandidatePublication(t *testing.T, database *db.DB, candidateID string) candidate.Publication {
	t.Helper()
	revision, err := database.GetCandidateRevision(context.Background(), candidateID)
	if err != nil || revision.Publication == nil {
		t.Fatalf("candidate publication = %#v, %v", revision, err)
	}
	return *revision.Publication
}

func claimServerCandidateWork(t *testing.T, database *db.DB, kind worklist.Kind) *db.CandidateWorkClaim {
	t.Helper()
	claim, err := database.ClaimCandidateWork(context.Background(), kind, "worker-"+string(kind), time.Minute, time.Now().UTC())
	if err != nil || claim == nil {
		t.Fatalf("claim %s = %#v, %v", kind, claim, err)
	}
	return claim
}

func generatorRun(id, sessionID string) agentruntime.CreateRun {
	return agentruntime.CreateRun{
		ID: id, Purpose: generator.RuntimePurpose, OwnerKind: "authoring-session", OwnerRef: sessionID,
		Model: "test-model", PromptVersion: generator.PromptVersion, ExecutionTimeout: time.Hour,
	}
}

func serverNodeSnapshot() candidate.ExecutionSnapshot {
	return candidate.ExecutionSnapshot{
		Runtime: challenge.RuntimeNode, Checkpoints: []candidate.CheckpointSnapshot{{ID: "service-ready", Node: "host"}},
		Node: &candidate.NodeRuntimeSnapshot{
			BaseImageFingerprint: testFingerprint('a'), ProfileRevision: "node-profile-v1", NetworkPolicyRevision: "node-network-v1",
			Nodes:     []candidate.NodeSnapshot{{Name: "host", Title: "Host"}},
			Resources: candidate.NodeResources{CPU: "1", Memory: "512MiB", Processes: 512, RootDisk: "5GiB"},
		},
	}
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
		Metadata:    authoring.Metadata{Title: title, Description: "intent description", Difficulty: "easy", Runtime: "node"},
		Overview:    overview,
		Checkpoints: []authoring.Checkpoint{{ID: "service-ready", Title: "Service ready", Markdown: "The service is ready.", Position: 1}},
	}
}

func writeAuthoringCandidate(t *testing.T, root, title string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "challenge.yaml"), "title: "+title+"\nruntime: node\ndifficulty: medium\ndescription: actual verified description\nnodes:\n  - name: host\n    title: Host\ncheckpoints:\n  - id: service-ready\n    title: Service ready\n    description: The service responds successfully.\n    hint: hints/service-ready.md\n    node: host\n")
	writeTestFile(t, filepath.Join(root, "problem.md"), "# Actual problem\n")
	writeTestFile(t, filepath.Join(root, "solution.md"), "# Actual solution\n<!-- checkpoint: service-ready -->\n")
	writeTestFile(t, filepath.Join(root, "hints", "service-ready.md"), "hint\n")
	writeTestFile(t, filepath.Join(root, "nodes", "host", "generate.sh"), "#!/bin/sh\n")
	writeTestFile(t, filepath.Join(root, "nodes", "host", "checks.sh"), "#!/bin/sh\n")
	writeTestFile(t, filepath.Join(root, "nodes", "host", "answer.sh"), "#!/bin/sh\n")
}

func archiveAuthoringCandidate(t *testing.T, root string) []byte {
	t.Helper()
	rootFS, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer rootFS.Close() //nolint:errcheck

	var archive bytes.Buffer
	gzipWriter := gzip.NewWriter(&archive)
	tarWriter := tar.NewWriter(gzipWriter)
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		file, err := rootFS.Open(relative)
		if err != nil {
			return err
		}
		content, readErr := io.ReadAll(file)
		closeErr := file.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		if err := tarWriter.WriteHeader(&tar.Header{Name: filepath.ToSlash(relative), Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
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

func testFingerprint(character byte) string {
	value := make([]byte, 64)
	for index := range value {
		value[index] = character
	}
	return string(value)
}
