package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/api"
	"github.com/breakfix/breakfix/internal/authoring"
	"github.com/breakfix/breakfix/internal/candidate"
	"github.com/breakfix/breakfix/internal/challenge"
	"github.com/breakfix/breakfix/internal/config"
	"github.com/breakfix/breakfix/internal/db"
	breakfixv1 "github.com/breakfix/breakfix/internal/k8s/apis/breakfix/v1"
	"github.com/breakfix/breakfix/internal/taxonomy"
	"github.com/breakfix/breakfix/internal/worklist"
	"github.com/gin-gonic/gin"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestMySpaceRequiresJWT(t *testing.T) {
	handler := newProgressTestHandler(t, nil)
	runCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	router, err := SetupRouter(runCtx, handler.db, handler.k8s, config.Config{DataDir: handler.dataDir, CRDNamespace: "breakfix-system", JWTSecret: "test-secret"}, nil, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/me/space", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401: %s", response.Code, response.Body.String())
	}
}

func TestMySpaceCombinesDurableFactsCRDsAndFilesystemMetadata(t *testing.T) {
	readyAt := time.Date(2026, time.July, 24, 10, 0, 0, 0, time.UTC)
	environment := testNodeEnvironment("environment-demo", breakfixv1.EnvironmentReady, &breakfixv1.CheckpointStatus{Results: []breakfixv1.CheckpointResultStatus{{
		ID: "complete", Passed: true, Summary: "done",
	}}})
	environment.UID = types.UID("environment-demo-uid")
	environment.Status.Environment.ReadyAt = &metav1.Time{Time: readyAt}
	environment.Status.Environment.ExpiresAt = &metav1.Time{Time: readyAt.Add(10 * time.Minute)}
	handler := newProgressTestHandler(t, []breakfixv1.NodeEnvironment{environment})
	ctx := context.Background()
	if err := handler.db.RecordChallengeAttempt(ctx, "u-demo", "demo", "environment-demo-uid", "node", readyAt); err != nil {
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
	if err := handler.db.RecordCheckpointFirstPass(ctx, db.CheckpointFirstPassEvent{EnvironmentUID: "environment-demo-uid", UserID: "u-demo", ChallengeID: "demo", ChallengeRevision: "revision-demo", CheckpointID: "complete", FirstPassedAt: readyAt.Add(time.Minute), Summary: "done"}); err != nil {
		t.Fatal(err)
	}
	createPublishedAuthoringSession(t, handler, "u-demo", "demo")

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
	if len(space.ActiveEnvironments) != 1 || space.ActiveEnvironments[0].Challenge.Title != "Demo" || space.ActiveEnvironments[0].CheckpointProgress.Passed != 1 {
		t.Fatalf("active environments = %#v", space.ActiveEnvironments)
	}
	if len(space.RecentLearning) != 1 || space.RecentLearning[0].Challenge.Id != "demo" || space.RecentLearning[0].CompletedAt == nil || space.RecentLearning[0].LearningSeconds != 75 || len(space.RecentLearning[0].CheckpointFirstPasses) != 1 {
		t.Fatalf("recent learning = %#v", space.RecentLearning)
	}
	if len(space.Authoring.Published) != 1 {
		t.Fatalf("published authoring = %#v", space.Authoring)
	}
	published := space.Authoring.Published[0]
	if published.AttemptedUsers != 1 || published.CompletedUsers != 1 || published.PassRate == nil || *published.PassRate != 1 {
		t.Fatalf("published authoring metrics = %#v", published)
	}
	if published.TaxonomyStatus != api.MySpacePublishedChallengeTaxonomyStatus("mapped") {
		t.Fatalf("published authoring taxonomy status = %q", published.TaxonomyStatus)
	}
}

func TestMySpaceQuotaCountsDeletingEnvironmentWithoutListingItAsActive(t *testing.T) {
	deletingAt := metav1.NewTime(time.Date(2026, time.July, 24, 11, 0, 0, 0, time.UTC))
	environment := testNodeEnvironment("deleting-environment", breakfixv1.EnvironmentReady, nil)
	environment.UID = types.UID("deleting-uid")
	environment.DeletionTimestamp = &deletingAt
	handler := newProgressTestHandler(t, []breakfixv1.NodeEnvironment{environment})
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
		{"environment-c", "node-challenge", "node"},
		{"environment-b", "k8s-challenge", "k8s"},
		{"environment-a", "removed-challenge", "node"},
	} {
		if err := handler.db.RecordChallengeAttempt(ctx, "u-demo", attempt.challengeID, attempt.environmentUID, attempt.runtime, readyAt); err != nil {
			t.Fatal(err)
		}
	}

	catalog := map[string]challenge.Entry{
		"node-challenge": {ID: "node-challenge", Title: "Linux node", Runtime: "node", Difficulty: "easy"},
		"k8s-challenge":  {ID: "k8s-challenge", Title: "Kubernetes", Runtime: "k8s", Difficulty: "medium"},
	}
	first, err := handler.mySpaceLearningFromCatalog(ctx, "u-demo", nil, db.LearningHistoryFilter{}, 1, readyAt.Add(time.Minute), catalog)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 1 || first.Items[0].Challenge.Id != "node-challenge" || first.NextCursor == nil {
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
	if len(second.Items) != 1 || second.Items[0].Challenge.Id != "k8s-challenge" || second.NextCursor != nil {
		t.Fatalf("second page = %#v", second)
	}

	runtime := api.GetMySpaceLearningParamsRuntimeK8s
	filter, err := mySpaceLearningFilter(api.GetMySpaceLearningParams{Runtime: &runtime})
	if err != nil {
		t.Fatal(err)
	}
	filtered, err := handler.mySpaceLearningFromCatalog(ctx, "u-demo", nil, filter, 20, readyAt.Add(time.Minute), catalog)
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered.Items) != 1 || filtered.Items[0].Challenge.Id != "k8s-challenge" {
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
	if err := handler.db.RecordChallengeAttempt(ctx, "u-demo", "demo", "learner-environment", "node", startedAt); err != nil {
		t.Fatal(err)
	}
	if err := handler.db.RecordChallengeCompletion(ctx, "u-demo", "demo", "learner-environment", startedAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	createPublishedAuthoringSession(t, handler, "u-author", "demo")
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

func TestMySpaceAuthoringProjectsTaxonomyStates(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name    string
		prepare func(t *testing.T, handler *Handler, entry challenge.Entry)
		want    api.MySpacePublishedChallengeTaxonomyStatus
	}{
		{
			name: "mapped",
			want: api.MySpacePublishedChallengeTaxonomyStatus("mapped"),
		},
		{
			name: "mapping without work item",
			prepare: func(t *testing.T, handler *Handler, entry challenge.Entry) {
				t.Helper()
				if err := os.Remove(filepath.Join(handler.dataDir, "taxonomy", "current")); err != nil {
					t.Fatal(err)
				}
			},
			want: api.MySpacePublishedChallengeTaxonomyStatus("mapping"),
		},
		{
			name: "retrying after technical failure budget",
			prepare: func(t *testing.T, handler *Handler, entry challenge.Entry) {
				t.Helper()
				if err := os.Remove(filepath.Join(handler.dataDir, "taxonomy", "current")); err != nil {
					t.Fatal(err)
				}
				mapping, _, err := handler.db.EnqueueTaxonomyMapping(context.Background(), taxonomy.TaxonomyMapping{
					ID:                "taxonomy-retrying",
					ChallengeID:       entry.ID,
					ChallengeRevision: entry.Revision,
					State:             taxonomy.MappingPending,
				})
				if err != nil {
					t.Fatal(err)
				}
				mapping.LastError = "temporary model failure"
				if err := handler.db.SaveTaxonomyMapping(context.Background(), *mapping); err != nil {
					t.Fatal(err)
				}
			},
			want: api.MySpacePublishedChallengeTaxonomyStatus("retrying"),
		},
		{
			name: "blocked after terminal work failure",
			prepare: func(t *testing.T, handler *Handler, entry challenge.Entry) {
				t.Helper()
				if err := os.Remove(filepath.Join(handler.dataDir, "taxonomy", "current")); err != nil {
					t.Fatal(err)
				}
				mapping, _, err := handler.db.EnqueueTaxonomyMapping(context.Background(), taxonomy.TaxonomyMapping{
					ID:                "taxonomy-blocked",
					ChallengeID:       entry.ID,
					ChallengeRevision: entry.Revision,
					State:             taxonomy.MappingPending,
				})
				if err != nil {
					t.Fatal(err)
				}
				mapping.State = taxonomy.MappingFailed
				mapping.LastError = "manual intervention required"
				if err := handler.db.SaveTaxonomyMapping(context.Background(), *mapping); err != nil {
					t.Fatal(err)
				}
			},
			want: api.MySpacePublishedChallengeTaxonomyStatus("blocked"),
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			handler := newProgressTestHandler(t, nil)
			entry, err := challenge.Get(handler.challengesDir, "demo")
			if err != nil {
				t.Fatal(err)
			}
			if tt.prepare != nil {
				tt.prepare(t, handler, *entry)
			}
			createPublishedAuthoringSession(t, handler, "u-demo", entry.ID)
			user, err := handler.db.GetUserByID("u-demo")
			if err != nil {
				t.Fatal(err)
			}
			space, err := handler.mySpace(ctx, user, mySpaceInitialLearningLimit)
			if err != nil {
				t.Fatal(err)
			}
			if len(space.Authoring.Published) != 1 || space.Authoring.Published[0].TaxonomyStatus != tt.want {
				t.Fatalf("taxonomy status = %#v, want %q", space.Authoring.Published, tt.want)
			}
		})
	}
}

func createPublishedAuthoringSession(t *testing.T, handler *Handler, userID, challengeID string) {
	t.Helper()
	ctx := context.Background()
	database := handler.db
	const sessionID = "authoring-demo"
	if _, err := database.CreateAuthoringSession(ctx, authoring.Session{ID: sessionID, UserID: userID}, authoring.Plan{}); err != nil {
		t.Fatal(err)
	}
	plan := authoring.Plan{
		Metadata:    authoring.Metadata{Title: "Demo", Description: "demo", Difficulty: "easy", Runtime: "node"},
		Overview:    "demo overview",
		Checkpoints: []authoring.Checkpoint{{ID: "complete", Title: "Complete", Markdown: "complete", Position: 1}},
	}
	revision, err := database.ReplaceAuthoringPlan(ctx, sessionID, userID, 0, plan, authoring.StateIntentReview)
	if err != nil {
		t.Fatal(err)
	}
	verifiedCandidate, artifact := seedVerifiedAuthoringCandidate(t, database, handler.dataDir, sessionID, userID, revision.Number, "generator-demo", "Demo")
	entry, err := challenge.Get(handler.challengesDir, challengeID)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := database.BeginAuthoringCandidatePublish(ctx, sessionID, userID, verifiedCandidate.ID, candidate.Publication{
		ChallengeID: challengeID, SourceSlug: entry.SourceSlug, TargetPath: entry.SourceSlug, RequestedAt: now,
	}, now); err != nil {
		t.Fatal(err)
	}
	claim, err := database.ClaimCandidateWork(ctx, worklist.KindChallengePublish, "my-space-publisher", time.Minute, time.Now().UTC())
	if err != nil || claim == nil {
		t.Fatalf("claim challenge publication = %#v, %v", claim, err)
	}
	if err := database.RecordCandidateChallengeArtifact(ctx, claim.Work, artifact, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := database.CompleteCandidateChallengePublish(ctx, claim.Work, artifact, entry.Revision, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
}
