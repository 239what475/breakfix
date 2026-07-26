package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/api"
	"github.com/breakfix/breakfix/internal/authoring"
	"github.com/breakfix/breakfix/internal/challenge"
	"github.com/breakfix/breakfix/internal/db"
	"github.com/breakfix/breakfix/internal/k8s"
	breakfixv1 "github.com/breakfix/breakfix/internal/k8s/apis/breakfix/v1"
	"github.com/breakfix/breakfix/internal/taxonomy"
	"github.com/gin-gonic/gin"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type authoringMessageRequest struct {
	Content string `json:"content"`
}

type authoringSessionResponse struct {
	ID              string                       `json:"id"`
	State           authoring.SessionState       `json:"state"`
	IntentRevision  int64                        `json:"intent_revision"`
	VisibleRevision int64                        `json:"visible_revision"`
	GenerationID    string                       `json:"generation_id,omitempty"`
	VerifyTaskID    string                       `json:"verify_task_id,omitempty"`
	UpdatedAt       string                       `json:"updated_at"`
	Intent          authoring.Plan               `json:"intent"`
	Artifact        *authoring.Artifact          `json:"artifact,omitempty"`
	Verified        *authoring.VerifiedChallenge `json:"verified,omitempty"`
	Verification    *authoring.Verification      `json:"verification,omitempty"`
	Messages        []authoring.Message          `json:"messages"`
	Assets          []authoring.Asset            `json:"assets"`
	Diff            []authoring.FileDiff         `json:"diff"`
}

// StartAuthoringReconciler keeps hidden verification-repair loops progressing
// even when the author closes the browser. It owns no state itself; every tick
// re-reads durable sessions and relies on the DB transitions for idempotency.
func (h *Handler) StartAuthoringReconciler(ctx context.Context) {
	if h.db == nil || h.k8s == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				ids, err := h.db.ListAuthoringSessionsNeedingSync(ctx)
				if err != nil {
					slog.Warn("list authoring sessions for sync", "err", err)
					continue
				}
				for _, id := range ids {
					if err := h.syncAuthoringSession(ctx, id); err != nil && !errors.Is(err, authoring.ErrNotFound) {
						slog.Warn("authoring sync failed", "session", id, "err", err)
					}
				}
			}
		}
	}()
}

func (h *Handler) CreateAuthoringSession(c *gin.Context) {
	user := h.requireUser(c)
	if user == nil {
		return
	}
	session, err := h.authoring.Create(c.Request.Context(), user.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("create authoring session: %v", err)})
		return
	}
	h.writeAuthoringSession(c, user, session.ID)
}

func (h *Handler) GetAuthoringSession(c *gin.Context, sessionID string) {
	user := h.requireUser(c)
	if user == nil {
		return
	}
	h.writeAuthoringSession(c, user, sessionID)
}

func (h *Handler) GetCurrentAuthoringSession(c *gin.Context) {
	user := h.requireUser(c)
	if user == nil {
		return
	}
	session, err := h.authoring.GetCurrent(c.Request.Context(), user.ID)
	if err != nil {
		h.writeAuthoringError(c, err)
		return
	}
	h.writeAuthoringSession(c, user, session.ID)
}

func (h *Handler) SendAuthoringMessage(c *gin.Context, sessionID string) {
	user := h.requireUser(c)
	if user == nil {
		return
	}
	var request authoringMessageRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: err.Error()})
		return
	}
	_, _, err := h.authoring.StartTurn(c.Request.Context(), user.ID, sessionID, request.Content)
	if err != nil {
		h.writeAuthoringError(c, err)
		return
	}
	h.writeAuthoringSession(c, user, sessionID)
}

func (h *Handler) ConfirmAuthoringGeneration(c *gin.Context, sessionID string) {
	user := h.requireUser(c)
	if user == nil {
		return
	}
	session, revision, _, err := h.authoring.Get(c.Request.Context(), user.ID, sessionID)
	if err != nil {
		h.writeAuthoringError(c, err)
		return
	}
	if session.State != authoring.StateIntentReview {
		c.JSON(http.StatusConflict, api.ErrorResponse{Error: "the current intent is not ready to generate and verify"})
		return
	}
	if err := h.startAuthoringGeneration(c.Request.Context(), user, session, revision, ""); err != nil {
		h.writeAuthoringError(c, err)
		return
	}
	h.writeAuthoringSession(c, user, sessionID)
}

func (h *Handler) PublishAuthoringRevision(c *gin.Context, sessionID string) {
	user := h.requireUser(c)
	if user == nil {
		return
	}
	if err := h.syncAuthoringSession(c.Request.Context(), sessionID); err != nil {
		h.writeAuthoringError(c, err)
		return
	}
	session, revision, _, err := h.authoring.Get(c.Request.Context(), user.ID, sessionID)
	if err != nil {
		h.writeAuthoringError(c, err)
		return
	}
	if session.State != authoring.StateAwaitingVerifiedReview || revision.Artifact == nil || revision.Verification == nil {
		c.JSON(http.StatusConflict, api.ErrorResponse{Error: "the current revision is not verified and ready to publish"})
		return
	}
	challengeID := challenge.NewID()
	stored, err := h.db.BeginPublish(c.Request.Context(), session.ID, user.ID, revision.Number, challengeID)
	if err != nil {
		h.writeAuthoringError(c, err)
		return
	}
	if err := h.promoteVerifiedRevision(c.Request.Context(), user, session, stored, challengeID); err != nil {
		// Promotion is an atomic filesystem rename followed by a DB transition.
		// If the latter fails, retain Publishing so syncAuthoringSession can finish
		// the durable record from the already promoted catalog directory. Reverting
		// to AwaitingVerifiedReview here would let a retry publish the same revision
		// under a second challenge ID.
		if _, catalogErr := challenge.Get(h.challengesDir, challengeID); catalogErr == nil {
			if syncErr := h.syncAuthoringSession(c.Request.Context(), session.ID); syncErr != nil {
				h.writeAuthoringError(c, syncErr)
				return
			}
			h.writeAuthoringSession(c, user, sessionID)
			return
		}
		_ = h.db.AbortPublish(c.Request.Context(), session.ID, user.ID, err.Error())
		h.writeAuthoringError(c, err)
		return
	}
	h.writeAuthoringSession(c, user, sessionID)
}

func (h *Handler) promoteVerifiedRevision(ctx context.Context, user *db.User, session *authoring.Session, revision *authoring.Revision, challengeID string) error {
	if revision == nil || revision.Artifact == nil || revision.Verification == nil {
		return authoring.ErrInvalidState
	}
	task, err := h.k8s.GetVerifyTask(ctx, h.crdNamespace, revision.Verification.TaskID)
	if err != nil {
		return fmt.Errorf("read verified task: %w", err)
	}
	if task.Status.Phase != breakfixv1.VerifyTaskSucceeded || strings.TrimSpace(task.Status.TempImage) == "" {
		return fmt.Errorf("verified task %s is not publishable", task.Name)
	}
	artifactDir, err := authoring.ArtifactPath(h.dataDir, revision.Artifact)
	if err != nil {
		return fmt.Errorf("resolve verified artifact: %w", err)
	}
	published, err := challenge.PromoteDirectory(h.challengesDir, artifactDir, challengeID, task.Status.TempImage)
	if err != nil {
		return fmt.Errorf("publish verified revision: %w", err)
	}
	if err := h.db.CompletePublish(ctx, session.ID, user.ID, revision.Number, challengeID); err != nil {
		return fmt.Errorf("record published revision: %w", err)
	}
	if err := challenge.RemoveSubmission(h.dataDir, revision.Artifact.SubmissionID); err != nil {
		slog.Warn("remove published authoring submission", "session", session.ID, "submission", revision.Artifact.SubmissionID, "err", err)
	}
	if h.taxonomyWorkflow != nil {
		baseRevision := ""
		if current, currentErr := h.taxonomy.LoadCurrent(); currentErr == nil {
			baseRevision = current.Revision
		} else if !errors.Is(currentErr, taxonomy.ErrNoCurrentRevision) {
			slog.Warn("read taxonomy before enqueue", "challenge", challengeID, "err", currentErr)
		}
		if _, enqueueErr := h.taxonomyWorkflow.EnqueueChallenge(ctx, *published, baseRevision); enqueueErr != nil {
			// Challenge publication is already durable. The periodic scanner will
			// retry this operational enqueue path after a transient DB error.
			slog.Warn("enqueue taxonomy mapping", "challenge", challengeID, "err", enqueueErr)
		}
	}
	return nil
}

func (h *Handler) startAuthoringGeneration(ctx context.Context, user *db.User, session *authoring.Session, revision *authoring.Revision, feedback string) error {
	if revision == nil || revision.Number != session.CurrentRevision {
		return authoring.ErrInvalidState
	}
	if err := revision.Plan.ValidateForGeneration(); err != nil {
		return err
	}
	if err := h.checkRegistryReady(ctx); err != nil {
		return err
	}
	genID := "gen-" + k8s.RandomID()
	if _, err := h.db.BeginGeneration(ctx, session.ID, user.ID, revision.Number, genID); err != nil {
		return err
	}
	active, err := h.db.GetAuthoringSession(ctx, session.ID, user.ID)
	if err != nil {
		return err
	}
	if err := h.createAuthoringGeneration(ctx, active, revision, genID, feedback); err != nil {
		// The durable state is already generating. Leave it there so the
		// reconciler can retry transient Kubernetes or registry failures without
		// exposing an internal failure revision to the author.
		slog.Warn("create authoring generation deferred", "session", session.ID, "generation", genID, "err", err)
	}
	return nil
}

func (h *Handler) restartAuthoringGeneration(ctx context.Context, session *authoring.Session, verification authoring.Verification) error {
	if session.State != authoring.StateGeneratingAndVerifying && session.State != authoring.StateRevisingAndVerifying {
		return nil
	}
	if err := h.checkRegistryReady(ctx); err != nil {
		return err
	}
	nextGenID := "gen-" + k8s.RandomID()
	feedback := ""
	if verification.Report != nil {
		feedback = verification.Report.FailureFeedback()
	}
	if feedback == "" {
		feedback = strings.TrimSpace(verification.Message)
	}
	active, err := h.db.RestartGeneration(ctx, session.ID, session.GenerationID, verification.TaskID, nextGenID, feedback)
	if err != nil {
		if errors.Is(err, authoring.ErrInvalidState) {
			return nil
		}
		return err
	}
	revision, err := h.db.GetAuthoringRevision(ctx, active.ID, active.CurrentRevision)
	if err != nil {
		return err
	}
	if err := h.createAuthoringGeneration(ctx, active, revision, nextGenID, active.PendingFeedback); err != nil {
		slog.Warn("restart authoring generation deferred", "session", active.ID, "generation", nextGenID, "err", err)
	}
	return nil
}

func (h *Handler) createAuthoringGeneration(ctx context.Context, session *authoring.Session, revision *authoring.Revision, generationID, feedback string) error {
	artifact, err := h.db.FindLatestAuthoringArtifact(ctx, session.ID, revision.Number)
	if err != nil {
		return err
	}
	env := map[string]string{
		"CHALLENGE_PLAN_JSON":            mustJSON(revision.Plan),
		"CHALLENGE_OUTPUT_DIR":           "/workspace/out",
		"REGISTRY_ADDR":                  h.registryAddr,
		"LAB_NAMESPACE":                  h.crdNamespace,
		"SERVER_INTERNAL_URL":            h.internalServerURL(),
		"SERVER_INTERNAL_API_KEY":        h.internalAPIKey,
		"GENERATION_ID":                  generationID,
		"GENERATION_AGENT_SESSION_ID":    session.WorkflowSessionID,
		"ANTHROPIC_BASE_URL":             h.llm.BaseURL,
		"ANTHROPIC_AUTH_TOKEN":           h.llm.APIKey,
		"ANTHROPIC_MODEL":                h.llm.Model,
		"ANTHROPIC_DEFAULT_OPUS_MODEL":   h.llm.Model,
		"ANTHROPIC_DEFAULT_SONNET_MODEL": h.llm.Model,
	}
	if session.WorkflowStarted {
		env["GENERATION_AGENT_RESUME"] = "true"
	}
	if artifact != nil && strings.TrimSpace(artifact.SubmissionID) != "" {
		env["GENERATION_BASE_SUBMISSION_ID"] = artifact.SubmissionID
	}
	if strings.TrimSpace(feedback) != "" {
		env["GENERATION_FEEDBACK"] = feedback
	}
	if h.registryInsecure {
		env["REGISTRY_INSECURE"] = "true"
	}
	secretName := generationID + "-env"
	secretData := make(map[string][]byte, len(env))
	for key, value := range env {
		secretData[key] = []byte(value)
	}
	if err := h.k8s.UpsertSecret(h.crdNamespace, secretName, secretData); err != nil {
		return fmt.Errorf("create generation secret: %w", err)
	}
	gen := &breakfixv1.Generation{
		ObjectMeta: metav1.ObjectMeta{Name: generationID, Namespace: h.crdNamespace},
		Spec: breakfixv1.GenerationSpec{
			Image:               h.generatorImage(),
			EnvSecretRef:        secretName,
			AuthoringSessionRef: session.ID,
			AuthoringRevision:   revision.Number,
		},
	}
	if _, err := h.k8s.CreateGeneration(ctx, h.crdNamespace, gen); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			_ = h.k8s.DeleteSecret(h.crdNamespace, secretName)
			return fmt.Errorf("create authoring generation: %w", err)
		}
	}
	if err := h.db.SetAuthoringWorkflowStarted(ctx, session.ID); err != nil {
		return fmt.Errorf("record workflow session start: %w", err)
	}
	return nil
}

// UploadGenerationArtifact is an internal handoff from a generator job. The
// archive is intentionally not extracted into the author workspace here: it
// first becomes a VerifyTask input, and only a successful task may materialize
// an author-visible artifact.
func (h *Handler) UploadGenerationArtifact(c *gin.Context) {
	if h.internalAPIKey == "" {
		c.JSON(http.StatusServiceUnavailable, api.ErrorResponse{Error: "internal upload disabled"})
		return
	}
	if c.GetHeader("X-Breakfix-Internal-Key") != h.internalAPIKey {
		c.JSON(http.StatusUnauthorized, api.ErrorResponse{Error: "invalid internal key"})
		return
	}
	genID := strings.TrimSpace(c.Param("id"))
	if genID == "" {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: "missing generation id"})
		return
	}
	gen, err := h.k8s.GetGeneration(c.Request.Context(), h.crdNamespace, genID)
	if err != nil {
		c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "generation job not found"})
		return
	}
	if strings.TrimSpace(gen.Spec.AuthoringSessionRef) == "" || gen.Spec.AuthoringRevision < 0 {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: "generation is not an authoring workflow"})
		return
	}
	file, _, err := c.Request.FormFile("artifact")
	if err != nil {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: "artifact file is required"})
		return
	}
	defer file.Close()
	submissionID := "sub-" + k8s.RandomID()
	if _, err := challenge.SaveSubmission(h.dataDir, submissionID, file); err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("save generated artifact: %v", err)})
		return
	}
	taskID := "vt-" + k8s.RandomID()
	task := &breakfixv1.VerifyTask{
		ObjectMeta: metav1.ObjectMeta{Name: taskID, Namespace: h.crdNamespace},
		Spec: breakfixv1.VerifyTaskSpec{
			Source:      breakfixv1.VerifyTaskSource{Ref: gen.Name},
			ChallengeID: challenge.NewID(),
			Submission:  breakfixv1.VerifyTaskSubmission{ID: submissionID},
		},
		Status: breakfixv1.VerifyTaskStatus{Phase: breakfixv1.VerifyTaskPending, Message: "generated artifact waiting for real verification"},
	}
	if _, err := h.k8s.CreateVerifyTask(c.Request.Context(), h.crdNamespace, task); err != nil {
		_ = challenge.RemoveSubmission(h.dataDir, submissionID)
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("create verify task: %v", err)})
		return
	}
	if err := h.db.AttachVerificationTask(c.Request.Context(), gen.Spec.AuthoringSessionRef, gen.Name, gen.Spec.AuthoringRevision, taskID); err != nil {
		_ = h.k8s.DeleteVerifyTask(c.Request.Context(), h.crdNamespace, taskID)
		_ = challenge.RemoveSubmission(h.dataDir, submissionID)
		c.JSON(http.StatusConflict, api.ErrorResponse{Error: err.Error()})
		return
	}
	gen.Status.Phase = breakfixv1.GenerationVerifying
	gen.Status.SubmissionID = submissionID
	gen.Status.ArtifactRevision = gen.Spec.AuthoringRevision
	gen.Status.VerifyTaskRef = taskID
	gen.Status.Message = "generated artifact submitted for real verification"
	if _, err := h.k8s.UpdateGenerationStatus(c.Request.Context(), h.crdNamespace, gen); err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("update generation status: %v", err)})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "verification_scheduled", "submission_id": submissionID, "verify_task_id": taskID})
}

func (h *Handler) writeAuthoringSession(c *gin.Context, user *db.User, sessionID string) {
	if err := h.syncAuthoringSession(c.Request.Context(), sessionID); err != nil && !errors.Is(err, authoring.ErrNotFound) {
		// Verification and repair failures are internal workflow details. Keep
		// serving the last durable, author-visible revision while the reconciler
		// retries instead of turning a polling request into an error artifact.
		slog.Warn("authoring sync deferred", "session", sessionID, "err", err)
	}
	session, revision, messages, err := h.authoring.Get(c.Request.Context(), user.ID, sessionID)
	if err != nil {
		h.writeAuthoringError(c, err)
		return
	}
	assets, err := authoring.ReadAssets(h.dataDir, revision.Artifact)
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: err.Error()})
		return
	}
	var previous *authoring.Artifact
	if revision.Number > 0 {
		previous, err = h.db.FindLatestAuthoringArtifact(c.Request.Context(), session.ID, revision.Number-1)
		if err != nil {
			c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("find previous verified artifact: %v", err)})
			return
		}
	}
	diff, err := authoring.DiffAssets(h.dataDir, revision.Artifact, previous)
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: err.Error()})
		return
	}
	var verified *authoring.VerifiedChallenge
	if revision.Artifact != nil {
		verified, err = authoring.ReadVerifiedChallenge(h.dataDir, revision.Artifact)
		if err != nil {
			c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: err.Error()})
			return
		}
	}
	c.JSON(http.StatusOK, authoringSessionResponse{
		ID: session.ID, State: session.State, IntentRevision: session.CurrentRevision, VisibleRevision: revision.Number,
		GenerationID: session.GenerationID, VerifyTaskID: session.VerifyTaskID,
		UpdatedAt: session.UpdatedAt.UTC().Format(time.RFC3339),
		Intent:    revision.Plan, Artifact: revision.Artifact, Verified: verified, Verification: revision.Verification,
		Messages: messages, Assets: assets, Diff: diff,
	})
}

func (h *Handler) syncAuthoringSession(ctx context.Context, sessionID string) error {
	session, err := h.db.GetAuthoringSessionInternal(ctx, sessionID)
	if err != nil {
		return err
	}
	if session.State == authoring.StatePublishing && session.PublishChallengeID != "" {
		// A filesystem promote completed but its DB completion was interrupted.
		// The catalog directory is already atomic, so finish the durable state.
		if _, err := challenge.Get(h.challengesDir, session.PublishChallengeID); err == nil {
			return h.db.CompletePublish(ctx, session.ID, session.UserID, session.VisibleRevision, session.PublishChallengeID)
		}
	}
	if h.k8s == nil {
		return nil
	}
	if strings.TrimSpace(session.VerifyTaskID) != "" {
		task, err := h.k8s.GetVerifyTask(ctx, h.crdNamespace, session.VerifyTaskID)
		if err == nil {
			verification := authoring.Verification{
				TaskID: task.Name, Phase: string(task.Status.Phase), Message: task.Status.Message,
				Report: authoringVerificationReport(task.Status.Report),
			}
			switch task.Status.Phase {
			case breakfixv1.VerifyTaskSucceeded:
				if session.State == authoring.StateAwaitingVerifiedReview || session.State == authoring.StatePublished || session.State == authoring.StatePublishing {
					return nil
				}
				artifact, err := h.storeVerifiedArtifact(ctx, session, task)
				if err != nil {
					return err
				}
				return h.db.CompleteVerification(ctx, session.ID, session.GenerationID, artifact, verification)
			case breakfixv1.VerifyTaskFailed:
				if task.Status.Report == nil || task.Status.Report.Class != breakfixv1.VerifyFailureArtifact {
					return h.db.RecordVerificationInfrastructureFailure(ctx, session.ID, session.GenerationID, verification)
				}
				if submissionID := strings.TrimSpace(task.Spec.Submission.ID); submissionID != "" {
					if err := challenge.RemoveSubmission(h.dataDir, submissionID); err != nil {
						slog.Warn("remove failed authoring submission", "session", session.ID, "submission", submissionID, "err", err)
					}
				}
				return h.restartAuthoringGeneration(ctx, session, verification)
			}
		}
	}
	if strings.TrimSpace(session.GenerationID) == "" {
		return nil
	}
	gen, err := h.k8s.GetGeneration(ctx, h.crdNamespace, session.GenerationID)
	if err != nil {
		if apierrors.IsNotFound(err) {
			revision, revisionErr := h.db.GetAuthoringRevision(ctx, session.ID, session.CurrentRevision)
			if revisionErr != nil {
				return revisionErr
			}
			return h.createAuthoringGeneration(ctx, session, revision, session.GenerationID, session.PendingFeedback)
		}
		return nil
	}
	if gen.Status.Phase == breakfixv1.GenerationFailed && strings.TrimSpace(session.VerifyTaskID) == "" {
		return h.restartAuthoringGeneration(ctx, session, authoring.Verification{
			Message: "生成任务未完成：" + strings.TrimSpace(gen.Status.Message),
		})
	}
	return nil
}

func (h *Handler) storeVerifiedArtifact(ctx context.Context, session *authoring.Session, task *breakfixv1.VerifyTask) (authoring.Artifact, error) {
	if task == nil || strings.TrimSpace(task.Spec.Submission.ID) == "" {
		return authoring.Artifact{}, authoring.ErrInvalidState
	}
	target := authoring.ArtifactDirectory(h.dataDir, session.ID, session.CurrentRevision)
	if _, err := os.Stat(target); err == nil {
		return authoring.Artifact{SubmissionID: task.Spec.Submission.ID, Directory: authoring.ArtifactRelativePath(session.ID, session.CurrentRevision), GenerationID: session.GenerationID}, nil
	} else if !os.IsNotExist(err) {
		return authoring.Artifact{}, fmt.Errorf("stat verified artifact: %w", err)
	}
	temp := authoring.TemporaryArtifactDirectory(h.dataDir, session.ID, task.Spec.Submission.ID)
	_ = os.RemoveAll(temp)
	if err := os.MkdirAll(temp, 0755); err != nil {
		return authoring.Artifact{}, fmt.Errorf("create verified artifact staging: %w", err)
	}
	defer os.RemoveAll(temp) //nolint:errcheck
	archive, err := os.Open(challenge.SubmissionPath(h.dataDir, task.Spec.Submission.ID))
	if err != nil {
		return authoring.Artifact{}, fmt.Errorf("open verified artifact: %w", err)
	}
	extractErr := challenge.ExtractTarGz(temp, archive)
	_ = archive.Close()
	if extractErr != nil {
		return authoring.Artifact{}, fmt.Errorf("extract verified artifact: %w", extractErr)
	}
	if _, err := challenge.ValidateSubmissionDir(temp); err != nil {
		return authoring.Artifact{}, fmt.Errorf("validate verified artifact: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return authoring.Artifact{}, fmt.Errorf("create verified artifact directory: %w", err)
	}
	if err := os.Rename(temp, target); err != nil {
		return authoring.Artifact{}, fmt.Errorf("store verified artifact: %w", err)
	}
	return authoring.Artifact{SubmissionID: task.Spec.Submission.ID, Directory: authoring.ArtifactRelativePath(session.ID, session.CurrentRevision), GenerationID: session.GenerationID}, nil
}

func authoringVerificationReport(report *breakfixv1.VerifyReport) *authoring.VerificationReport {
	if report == nil {
		return nil
	}
	value := &authoring.VerificationReport{
		Class:             authoring.VerificationFailureClass(report.Class),
		BuildPassed:       report.BuildPassed,
		AnswerPassed:      report.AnswerPassed,
		CheckpointsPassed: report.CheckpointsPassed,
		Summary:           report.Summary,
		Issues:            make([]authoring.VerificationIssue, 0, len(report.Issues)),
	}
	for _, issue := range report.Issues {
		value.Issues = append(value.Issues, authoring.VerificationIssue{Code: issue.Code, Message: issue.Message})
	}
	return value
}

func (h *Handler) writeAuthoringError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, authoring.ErrNotFound):
		c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "authoring session not found"})
	case errors.Is(err, authoring.ErrVersionConflict), errors.Is(err, authoring.ErrInvalidState):
		c.JSON(http.StatusConflict, api.ErrorResponse{Error: err.Error()})
	default:
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: err.Error()})
	}
}
