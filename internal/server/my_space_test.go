package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/api"
	"github.com/breakfix/breakfix/internal/authoring"
	"github.com/breakfix/breakfix/internal/challenge"
	"github.com/breakfix/breakfix/internal/config"
	"github.com/breakfix/breakfix/internal/db"
	breakfixv1 "github.com/breakfix/breakfix/internal/k8s/apis/breakfix/v1"
	"github.com/gin-gonic/gin"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestMySpaceRequiresJWT(t *testing.T) {
	handler := newProgressTestHandler(t, nil)
	router := SetupRouter(context.Background(), handler.db, handler.k8s, config.Config{DataDir: handler.dataDir, CRDNamespace: "breakfix-system", JWTSecret: "test-secret"}, nil)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/me/space", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401: %s", response.Code, response.Body.String())
	}
}

func TestMySpaceCombinesDurableFactsCRDsAndFilesystemMetadata(t *testing.T) {
	readyAt := time.Date(2026, time.July, 24, 10, 0, 0, 0, time.UTC)
	handler := newProgressTestHandler(t, []breakfixv1.ContainerEnvironment{{
		ObjectMeta: metav1.ObjectMeta{Name: "environment-demo", UID: types.UID("environment-demo-uid")},
		Spec:       breakfixv1.CommonEnvironmentSpec{ChallengeRef: "demo", UserRef: "u-demo"},
		Status: breakfixv1.CommonEnvironmentStatus{
			Phase:     breakfixv1.EnvironmentReady,
			ReadyAt:   &metav1.Time{Time: readyAt},
			ExpiresAt: &metav1.Time{Time: readyAt.Add(10 * time.Minute)},
			Checkpoints: &breakfixv1.CheckpointStatus{Results: []breakfixv1.CheckpointResultStatus{{
				ID: "complete", Passed: true, Summary: "done",
			}}},
		},
	}})
	ctx := context.Background()
	if err := handler.db.RecordChallengeAttempt(ctx, "u-demo", "demo", "environment-demo-uid", "container", readyAt); err != nil {
		t.Fatal(err)
	}
	if err := handler.db.OpenTerminalConnection(ctx, db.TerminalConnection{ID: "terminal-demo", EnvironmentUID: "environment-demo-uid", UserID: "u-demo", ChallengeID: "demo", ServerInstanceID: "server-test", ConnectedAt: readyAt}); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.db.CloseTerminalConnection(ctx, "terminal-demo", readyAt.Add(75*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.db.FinishTerminalUsageSession(ctx, "environment-demo-uid", readyAt.Add(75*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := handler.db.RecordChallengeCompletion(ctx, "u-demo", "demo", "environment-demo-uid", readyAt.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	createPublishedAuthoringSession(t, handler.db, "u-demo", "demo")

	response := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(response)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/me/space", nil)
	c.Set("user_id", "u-demo")
	handler.GetMySpace(c)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	var space api.MySpace
	if err := json.Unmarshal(response.Body.Bytes(), &space); err != nil {
		t.Fatal(err)
	}
	if space.Summary.CompletedCount != 1 || space.Summary.AttemptedCount != 1 || space.Summary.TerminalLearningSeconds != 75 {
		t.Fatalf("learning summary = %#v", space.Summary)
	}
	if space.Summary.InProgressEnvironmentCount != 1 || space.Summary.EnvironmentQuota.Occupied != 1 {
		t.Fatalf("environment summary = %#v", space.Summary)
	}
	if len(space.ActiveEnvironments) != 1 || space.ActiveEnvironments[0].Challenge.Title != "Demo" || space.ActiveEnvironments[0].CheckpointProgress.Passed == nil || *space.ActiveEnvironments[0].CheckpointProgress.Passed != 1 {
		t.Fatalf("active environments = %#v", space.ActiveEnvironments)
	}
	if len(space.RecentLearning) != 1 || space.RecentLearning[0].Challenge.Id != "demo" || space.RecentLearning[0].CompletedAt == nil || space.RecentLearning[0].LearningSeconds != 75 {
		t.Fatalf("recent learning = %#v", space.RecentLearning)
	}
	if len(space.Authoring.Published) != 1 {
		t.Fatalf("published authoring = %#v", space.Authoring)
	}
	published := space.Authoring.Published[0]
	if published.AttemptedUsers != 1 || published.CompletedUsers != 1 || published.PassRate == nil || *published.PassRate != 1 {
		t.Fatalf("published authoring metrics = %#v", published)
	}
}

func TestMySpaceQuotaCountsDeletingEnvironmentWithoutListingItAsActive(t *testing.T) {
	deletingAt := metav1.NewTime(time.Date(2026, time.July, 24, 11, 0, 0, 0, time.UTC))
	handler := newProgressTestHandler(t, []breakfixv1.ContainerEnvironment{{
		ObjectMeta: metav1.ObjectMeta{Name: "deleting-environment", UID: types.UID("deleting-uid"), DeletionTimestamp: &deletingAt},
		Spec:       breakfixv1.CommonEnvironmentSpec{ChallengeRef: "demo", UserRef: "u-demo"},
		Status:     breakfixv1.CommonEnvironmentStatus{Phase: breakfixv1.EnvironmentReady},
	}})
	response := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(response)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/me/space", nil)
	c.Set("user_id", "u-demo")
	handler.GetMySpace(c)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	var space api.MySpace
	if err := json.Unmarshal(response.Body.Bytes(), &space); err != nil {
		t.Fatal(err)
	}
	if space.Summary.EnvironmentQuota.Occupied != 1 || space.Summary.InProgressEnvironmentCount != 0 || len(space.ActiveEnvironments) != 0 {
		t.Fatalf("deleting environment summary = %#v, active = %#v", space.Summary, space.ActiveEnvironments)
	}
}

func TestMySpaceLearningFiltersCatalogAndUsesStableCursor(t *testing.T) {
	handler := newProgressTestHandler(t, nil)
	ctx := context.Background()
	readyAt := time.Date(2026, time.July, 24, 12, 0, 0, 0, time.UTC)
	for _, attempt := range []struct {
		environmentUID string
		challengeID    string
		runtime        string
	}{
		{"environment-c", "container-challenge", "container"},
		{"environment-b", "vcluster-challenge", "vcluster"},
		{"environment-a", "removed-challenge", "container"},
	} {
		if err := handler.db.RecordChallengeAttempt(ctx, "u-demo", attempt.challengeID, attempt.environmentUID, attempt.runtime, readyAt); err != nil {
			t.Fatal(err)
		}
	}

	catalog := map[string]challenge.Entry{
		"container-challenge": {ID: "container-challenge", Title: "Container", Runtime: "container", Difficulty: "easy"},
		"vcluster-challenge":  {ID: "vcluster-challenge", Title: "VCluster", Runtime: "vcluster", Difficulty: "medium"},
	}
	first, err := handler.mySpaceLearningFromCatalog(ctx, "u-demo", nil, db.LearningHistoryFilter{}, 1, readyAt.Add(time.Minute), catalog)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 1 || first.Items[0].Challenge.Id != "container-challenge" || first.NextCursor == nil {
		t.Fatalf("first page = %#v", first)
	}
	cursor, err := parseMySpaceLearningCursor(first.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	second, err := handler.mySpaceLearningFromCatalog(ctx, "u-demo", cursor, db.LearningHistoryFilter{}, 1, readyAt.Add(time.Minute), catalog)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 1 || second.Items[0].Challenge.Id != "vcluster-challenge" || second.NextCursor != nil {
		t.Fatalf("second page = %#v", second)
	}

	runtime := api.GetMySpaceLearningParamsRuntimeVcluster
	filter, err := mySpaceLearningFilter(api.GetMySpaceLearningParams{Runtime: &runtime})
	if err != nil {
		t.Fatal(err)
	}
	filtered, err := handler.mySpaceLearningFromCatalog(ctx, "u-demo", nil, filter, 20, readyAt.Add(time.Minute), catalog)
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered.Items) != 1 || filtered.Items[0].Challenge.Id != "vcluster-challenge" {
		t.Fatalf("runtime filtered history = %#v", filtered)
	}
}

func TestMySpaceAuthoringReturnsOnlyAnonymousLearnerAggregates(t *testing.T) {
	handler := newProgressTestHandler(t, nil)
	ctx := context.Background()
	if _, err := handler.db.CreateUserWithAuth("u-author", "author", "hash", "totp"); err != nil {
		t.Fatal(err)
	}
	startedAt := time.Date(2026, time.July, 24, 13, 0, 0, 0, time.UTC)
	if err := handler.db.RecordChallengeAttempt(ctx, "u-demo", "demo", "learner-environment", "container", startedAt); err != nil {
		t.Fatal(err)
	}
	if err := handler.db.RecordChallengeCompletion(ctx, "u-demo", "demo", "learner-environment", startedAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	createPublishedAuthoringSession(t, handler.db, "u-author", "demo")
	author, err := handler.db.GetUserByID("u-author")
	if err != nil {
		t.Fatal(err)
	}
	space, err := handler.mySpace(ctx, author, mySpaceInitialLearningLimit)
	if err != nil {
		t.Fatal(err)
	}
	if len(space.Authoring.Published) != 1 {
		t.Fatalf("published authoring = %#v", space.Authoring)
	}
	published := space.Authoring.Published[0]
	if published.AttemptedUsers != 1 || published.CompletedUsers != 1 || published.PassRate == nil || *published.PassRate != 1 {
		t.Fatalf("anonymous aggregate = %#v", published)
	}
	payload, err := json.Marshal(space.Authoring)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "u-demo") || strings.Contains(string(payload), "learner-environment") {
		t.Fatalf("authoring response leaked learner identity: %s", payload)
	}
}

func createPublishedAuthoringSession(t *testing.T, database *db.DB, userID, challengeID string) {
	t.Helper()
	ctx := context.Background()
	const sessionID = "authoring-demo"
	if _, err := database.CreateAuthoringSession(ctx, authoring.Session{ID: sessionID, UserID: userID, AgentSessionID: "agent", WorkflowSessionID: "workflow"}, authoring.Plan{}); err != nil {
		t.Fatal(err)
	}
	plan := authoring.Plan{
		Metadata:    authoring.Metadata{Title: "Demo", Description: "demo", Difficulty: "easy", Runtime: "container"},
		Overview:    "demo overview",
		Checkpoints: []authoring.Checkpoint{{ID: "complete", Title: "Complete", Markdown: "complete", Position: 1}},
	}
	revision, err := database.ReplaceAuthoringPlan(ctx, sessionID, userID, 0, plan, authoring.StateIntentReview)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.BeginGeneration(ctx, sessionID, userID, revision.Number, "generation-demo"); err != nil {
		t.Fatal(err)
	}
	if err := database.AttachVerificationTask(ctx, sessionID, "generation-demo", revision.Number, "verify-demo"); err != nil {
		t.Fatal(err)
	}
	if err := database.CompleteVerification(ctx, sessionID, "generation-demo", authoring.Artifact{SubmissionID: "submission-demo", Directory: "authoring/demo", GenerationID: "generation-demo"}, authoring.Verification{TaskID: "verify-demo", Phase: "Succeeded"}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.BeginPublish(ctx, sessionID, userID, revision.Number, challengeID); err != nil {
		t.Fatal(err)
	}
	if err := database.CompletePublish(ctx, sessionID, userID, revision.Number, challengeID); err != nil {
		t.Fatal(err)
	}
}
