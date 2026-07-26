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

	"github.com/breakfix/breakfix/internal/agentruntime"
	"github.com/breakfix/breakfix/internal/api"
	"github.com/breakfix/breakfix/internal/authoring"
	"github.com/breakfix/breakfix/internal/challenge"
	"github.com/breakfix/breakfix/internal/db"
	"github.com/breakfix/breakfix/internal/generator"
	breakfixv1 "github.com/breakfix/breakfix/internal/k8s/apis/breakfix/v1"
	"github.com/breakfix/breakfix/internal/taxonomy"
	"github.com/gin-gonic/gin"
)

type authoringMessageRequest struct {
	Content string `json:"content"`
}

type authoringSessionResponse struct {
	ID              string                       `json:"id"`
	State           authoring.SessionState       `json:"state"`
	IntentRevision  int64                        `json:"intent_revision"`
	VisibleRevision int64                        `json:"visible_revision"`
	GeneratorRunID  string                       `json:"generator_run_id,omitempty"`
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
	session, _, _, err := h.authoring.Get(c.Request.Context(), user.ID, sessionID)
	if err != nil {
		h.writeAuthoringError(c, err)
		return
	}
	if session.State != authoring.StateIntentReview && session.State != authoring.StateRevisingAndVerifying {
		c.JSON(http.StatusConflict, api.ErrorResponse{Error: "the current intent is not ready to generate and verify"})
		return
	}
	revision, err := h.db.GetAuthoringRevision(c.Request.Context(), session.ID, session.CurrentRevision)
	if err != nil {
		h.writeAuthoringError(c, err)
		return
	}
	input := generator.RunInput{}
	if session.State == authoring.StateRevisingAndVerifying {
		previous, artifactErr := h.db.FindLatestAuthoringArtifact(c.Request.Context(), session.ID, revision.Number-1)
		if artifactErr != nil {
			h.writeAuthoringError(c, artifactErr)
			return
		}
		if previous == nil || strings.TrimSpace(previous.SubmissionID) == "" {
			h.writeAuthoringError(c, authoring.ErrInvalidState)
			return
		}
		input.SeedSubmissionID = previous.SubmissionID
		if session.GeneratorSessionID != "" {
			if err := h.generatorWorkspace.Cleanup(c.Request.Context(), session.GeneratorSessionID); err != nil {
				h.writeAuthoringError(c, fmt.Errorf("cleanup superseded generator workspace: %w", err))
				return
			}
		}
	}
	if err := h.startAuthoringGeneratorRun(c.Request.Context(), user, session, revision, input); err != nil {
		h.writeAuthoringError(c, err)
		return
	}
	h.writeAuthoringSession(c, user, sessionID)
}

// startAuthoringGeneratorRun creates a durable Agent Run. It intentionally
// does not create a Kubernetes generator resource or hand model credentials
// to a Job: the Agent Worker will claim this run and reach its Server-owned
// OpenSandbox workspace through fenced internal APIs.
func (h *Handler) startAuthoringGeneratorRun(ctx context.Context, user *db.User, session *authoring.Session, revision *authoring.Revision, input generator.RunInput) error {
	if h.db == nil || h.k8s == nil || h.generatorWorkspace == nil || h.generatorSandbox == nil {
		return errors.New("generator runtime is unavailable")
	}
	if user == nil || session == nil || revision == nil || revision.Number != session.CurrentRevision {
		return authoring.ErrInvalidState
	}
	if err := revision.Plan.ValidateForGeneration(); err != nil {
		return err
	}
	input.AuthoringSessionID = session.ID
	input.Revision = revision.Number
	now := time.Now().UTC()
	_, _, err := h.db.StartGeneratorRun(ctx, session.ID, user.ID, revision.Number, agentruntime.CreateRun{
		ID:            generator.NewRunID(),
		Purpose:       generator.RuntimePurpose,
		OwnerKind:     "authoring-session",
		OwnerRef:      session.ID,
		Model:         h.llm.Model,
		PromptVersion: generator.PromptVersion,
		DeadlineAt:    now.Add(generator.RunDeadline),
	}, input)
	return err
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

// restartAuthoringGeneratorRun starts the next semantic Generator Run after a
// real artifact failure. It deliberately retains the failed immutable
// submission: the Server resets the same Generator Session workspace from it
// before the next Worker attempt can make changes.
func (h *Handler) restartAuthoringGeneratorRun(ctx context.Context, session *authoring.Session, task *breakfixv1.VerifyTask, verification authoring.Verification) error {
	if session == nil || task == nil || strings.TrimSpace(session.GeneratorRunID) == "" {
		return authoring.ErrInvalidState
	}
	if task.Status.Report == nil || task.Status.Report.Class != breakfixv1.VerifyFailureArtifact {
		return authoring.ErrInvalidState
	}
	feedback, err := generatorFeedbackFromVerification(verification)
	if err != nil {
		return err
	}
	revision, err := h.db.GetAuthoringRevision(ctx, session.ID, session.CurrentRevision)
	if err != nil {
		return err
	}
	return h.startAuthoringGeneratorRun(ctx, &db.User{ID: session.UserID}, session, revision, generator.RunInput{
		SeedSubmissionID: task.Spec.Submission.ID,
		VerifyTaskID:     task.Name,
		Feedback:         feedback,
	})
}

func generatorFeedbackFromVerification(verification authoring.Verification) (generator.Feedback, error) {
	if verification.Report == nil || verification.Report.Class != authoring.VerificationFailureArtifact {
		return generator.Feedback{}, errors.New("artifact verification result requires a structured artifact report")
	}
	feedback := generator.Feedback{
		BuildPassed:       verification.Report.BuildPassed,
		AnswerPassed:      verification.Report.AnswerPassed,
		CheckpointsPassed: verification.Report.CheckpointsPassed,
		Summary:           verification.Report.Summary,
		Issues:            make([]generator.Issue, 0, len(verification.Report.Issues)),
	}
	for _, issue := range verification.Report.Issues {
		feedback.Issues = append(feedback.Issues, generator.Issue{Code: issue.Code, Message: issue.Message})
	}
	if err := feedback.Validate(); err != nil {
		return generator.Feedback{}, fmt.Errorf("validate generator verification feedback: %w", err)
	}
	return feedback, nil
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
		GeneratorRunID: session.GeneratorRunID, VerifyTaskID: session.VerifyTaskID,
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
	if strings.TrimSpace(session.GeneratorRunID) == "" {
		return nil
	}
	return h.syncGeneratorRun(ctx, session)
}

// syncGeneratorRun observes only the VerifyTask produced by the durable
// Generator Run. Before the Worker submits a candidate there is deliberately
// no task to inspect; this must never fall through to a retired generator
// resource recovery path.
func (h *Handler) syncGeneratorRun(ctx context.Context, session *authoring.Session) error {
	if session == nil || strings.TrimSpace(session.GeneratorRunID) == "" {
		return authoring.ErrInvalidState
	}
	if strings.TrimSpace(session.VerifyTaskID) == "" {
		return nil
	}
	task, err := h.k8s.GetVerifyTask(ctx, h.crdNamespace, session.VerifyTaskID)
	if err != nil {
		return fmt.Errorf("read generator verify task: %w", err)
	}
	run, err := h.db.GetGeneratorRunByVerifyTask(ctx, task.Name)
	if err != nil {
		return fmt.Errorf("read generator run for verify task: %w", err)
	}
	if run.RunID != session.GeneratorRunID || run.SubmissionID == "" || task.Spec.Source.Ref != run.RunID || task.Spec.Submission.ID != run.SubmissionID {
		return errors.New("generator verify task does not match the active generator run")
	}
	verification := authoring.Verification{
		TaskID: task.Name, Phase: string(task.Status.Phase), Message: task.Status.Message,
		Report: authoringVerificationReport(task.Status.Report),
	}
	switch task.Status.Phase {
	case breakfixv1.VerifyTaskSucceeded:
		if session.State == authoring.StateAwaitingVerifiedReview || session.State == authoring.StatePublished || session.State == authoring.StatePublishing {
			if session.GeneratorSessionID != "" && h.generatorWorkspace != nil {
				return h.generatorWorkspace.Cleanup(ctx, session.GeneratorSessionID)
			}
			return nil
		}
		artifact, err := h.storeVerifiedArtifact(ctx, session, task)
		if err != nil {
			return err
		}
		if err := h.db.CompleteGeneratorVerification(ctx, session.ID, session.GeneratorRunID, artifact, verification); err != nil {
			return err
		}
		if session.GeneratorSessionID != "" && h.generatorWorkspace != nil {
			return h.generatorWorkspace.Cleanup(ctx, session.GeneratorSessionID)
		}
		return nil
	case breakfixv1.VerifyTaskFailed:
		if task.Status.Report == nil || task.Status.Report.Class != breakfixv1.VerifyFailureArtifact {
			return h.db.RecordGeneratorVerificationInfrastructureFailure(ctx, session.ID, session.GeneratorRunID, verification)
		}
		return h.restartAuthoringGeneratorRun(ctx, session, task, verification)
	default:
		return nil
	}
}

func (h *Handler) storeVerifiedArtifact(ctx context.Context, session *authoring.Session, task *breakfixv1.VerifyTask) (authoring.Artifact, error) {
	if task == nil || strings.TrimSpace(task.Spec.Submission.ID) == "" {
		return authoring.Artifact{}, authoring.ErrInvalidState
	}
	target := authoring.ArtifactDirectory(h.dataDir, session.ID, session.CurrentRevision)
	if _, err := os.Stat(target); err == nil {
		return authoring.Artifact{SubmissionID: task.Spec.Submission.ID, Directory: authoring.ArtifactRelativePath(session.ID, session.CurrentRevision), GeneratorRunID: session.GeneratorRunID}, nil
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
	return authoring.Artifact{SubmissionID: task.Spec.Submission.ID, Directory: authoring.ArtifactRelativePath(session.ID, session.CurrentRevision), GeneratorRunID: session.GeneratorRunID}, nil
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
