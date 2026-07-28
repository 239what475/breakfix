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
	var request api.AuthoringMessageRequest
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
	if session.State == authoring.StateRevisingAndVerifying {
		if err := h.startRevisedAuthoringGeneratorRun(c.Request.Context(), session); err != nil {
			h.writeAuthoringError(c, err)
			return
		}
		h.writeAuthoringSession(c, user, sessionID)
		return
	}
	revision, err := h.db.GetAuthoringRevision(c.Request.Context(), session.ID, session.CurrentRevision)
	if err != nil {
		h.writeAuthoringError(c, err)
		return
	}
	if err := h.startAuthoringGeneratorRun(c.Request.Context(), user, session, revision, generator.RunInput{}); err != nil {
		h.writeAuthoringError(c, err)
		return
	}
	h.writeAuthoringSession(c, user, sessionID)
}

// startRevisedAuthoringGeneratorRun turns an author-approved change to an
// already verified challenge into the next verification cycle. This is called
// by the reconciler as well as the explicit endpoint, so StartGeneratorRun is
// deliberately idempotent for a revision that another caller already started.
func (h *Handler) startRevisedAuthoringGeneratorRun(ctx context.Context, session *authoring.Session) error {
	if session == nil || session.State != authoring.StateRevisingAndVerifying {
		return authoring.ErrInvalidState
	}
	revision, err := h.db.GetAuthoringRevision(ctx, session.ID, session.CurrentRevision)
	if err != nil {
		return err
	}
	previous, err := h.db.FindLatestAuthoringArtifact(ctx, session.ID, revision.Number-1)
	if err != nil {
		return err
	}
	if previous == nil || strings.TrimSpace(previous.SubmissionID) == "" {
		return authoring.ErrInvalidState
	}
	return h.startAuthoringGeneratorRun(ctx, &db.User{ID: session.UserID}, session, revision, generator.RunInput{
		SeedSubmissionID: previous.SubmissionID,
	})
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
	h.enqueuePublishedChallengeTaxonomy(ctx, *published)
	return nil
}

// enqueuePublishedChallengeTaxonomy makes taxonomy classification prompt after
// promotion. The scanner remains the convergence path if this best-effort
// enqueue is interrupted after the filesystem rename.
func (h *Handler) enqueuePublishedChallengeTaxonomy(ctx context.Context, published challenge.Entry) {
	if h.taxonomyWorkflow == nil {
		return
	}
	baseRevision := ""
	if current, err := h.taxonomy.LoadCurrent(); err == nil {
		baseRevision = current.Revision
	} else if !errors.Is(err, taxonomy.ErrNoCurrentRevision) {
		slog.Warn("read taxonomy before enqueue", "challenge", published.ID, "err", err)
	}
	if _, err := h.taxonomyWorkflow.EnqueueChallenge(ctx, published, baseRevision); err != nil {
		// Challenge publication is already durable. The periodic scanner retries
		// this operational enqueue path after a transient database failure.
		slog.Warn("enqueue taxonomy mapping", "challenge", published.ID, "err", err)
	}
}

// restartAuthoringGeneratorRun starts the next semantic Generator Run after a
// real artifact failure. It deliberately retains the failed immutable
// submission: the next Run initializes a new workspace from it before its
// Worker attempt can make changes.
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
	authoringTurnActive := false
	if active, err := h.db.GetActiveRunForSession(c.Request.Context(), session.RuntimeSessionID); err == nil {
		authoringTurnActive = active.Purpose == "authoring" && active.OwnerKind == "authoring-session" && active.OwnerRef == session.ID
	} else if !errors.Is(err, agentruntime.ErrNotFound) {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("read active authoring run: %v", err)})
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
	c.JSON(http.StatusOK, toAPIAuthoringSession(session, revision, authoringTurnActive, messages, assets, diff, verified))
}

func toAPIAuthoringSession(session *authoring.Session, revision *authoring.Revision, turnActive bool, messages []authoring.Message, assets []authoring.Asset, diff []authoring.FileDiff, verified *authoring.VerifiedChallenge) api.AuthoringSession {
	return api.AuthoringSession{
		Artifact:            toAPIAuthoringArtifact(revision.Artifact),
		Assets:              toAPIAuthoringAssets(assets),
		AuthoringTurnActive: turnActive,
		Diff:                toAPIAuthoringFileDiffs(diff),
		GeneratorRunId:      optionalString(session.GeneratorRunID),
		Id:                  session.ID,
		Intent:              toAPIAuthoringPlan(revision.Plan),
		IntentRevision:      int(session.CurrentRevision),
		Messages:            toAPIAuthoringMessages(messages),
		PublishChallengeId:  optionalString(session.PublishChallengeID),
		State:               api.AuthoringSessionState(session.State),
		UpdatedAt:           session.UpdatedAt.UTC(),
		Verification:        toAPIAuthoringVerification(revision.Verification),
		Verified:            toAPIVerifiedChallenge(verified),
		VerifyTaskId:        optionalString(session.VerifyTaskID),
		VisibleRevision:     int(revision.Number),
	}
}

func toAPIAuthoringPlan(plan authoring.Plan) api.AuthoringPlan {
	checkpoints := make([]api.AuthoringCheckpoint, 0, len(plan.Checkpoints))
	for _, checkpoint := range plan.Checkpoints {
		checkpoints = append(checkpoints, api.AuthoringCheckpoint{
			Id:       checkpoint.ID,
			Markdown: checkpoint.Markdown,
			Position: int(checkpoint.Position),
			Title:    checkpoint.Title,
		})
	}
	return api.AuthoringPlan{
		Checkpoints: checkpoints,
		Metadata:    toAPIAuthoringMetadata(plan.Metadata),
		Overview:    plan.Overview,
	}
}

func toAPIAuthoringMetadata(metadata authoring.Metadata) api.AuthoringMetadata {
	return api.AuthoringMetadata{
		Description: metadata.Description,
		Difficulty:  api.AuthoringMetadataDifficulty(metadata.Difficulty),
		Runtime:     api.AuthoringMetadataRuntime(metadata.Runtime),
		Title:       metadata.Title,
	}
}

func toAPIAuthoringArtifact(artifact *authoring.Artifact) *api.AuthoringArtifact {
	if artifact == nil {
		return nil
	}
	return &api.AuthoringArtifact{
		Directory:      artifact.Directory,
		GeneratorRunId: artifact.GeneratorRunID,
		SubmissionId:   artifact.SubmissionID,
	}
}

func toAPIVerifiedChallenge(challenge *authoring.VerifiedChallenge) *api.VerifiedChallenge {
	if challenge == nil {
		return nil
	}
	checkpoints := make([]api.VerifiedCheckpoint, 0, len(challenge.Checkpoints))
	for _, checkpoint := range challenge.Checkpoints {
		checkpoints = append(checkpoints, api.VerifiedCheckpoint{
			DependsOn:   optionalSlice(checkpoint.DependsOn),
			Description: checkpoint.Description,
			Hint:        optionalString(checkpoint.Hint),
			Id:          checkpoint.ID,
			Title:       checkpoint.Title,
		})
	}
	return &api.VerifiedChallenge{
		Checkpoints: checkpoints,
		Metadata:    toAPIAuthoringMetadata(challenge.Metadata),
	}
}

func toAPIAuthoringVerification(verification *authoring.Verification) *api.AuthoringVerification {
	if verification == nil {
		return nil
	}
	return &api.AuthoringVerification{
		ChallengeId: optionalString(verification.ChallengeID),
		Message:     verification.Message,
		Phase:       verification.Phase,
		Report:      toAPIAuthoringVerificationReport(verification.Report),
		TaskId:      verification.TaskID,
	}
}

func toAPIAuthoringVerificationReport(report *authoring.VerificationReport) *api.AuthoringVerificationReport {
	if report == nil {
		return nil
	}
	issues := make([]api.AuthoringVerificationIssue, 0, len(report.Issues))
	for _, issue := range report.Issues {
		issues = append(issues, api.AuthoringVerificationIssue{Code: issue.Code, Message: issue.Message})
	}
	var class *api.AuthoringVerificationReportClass
	if report.Class != "" {
		value := api.AuthoringVerificationReportClass(report.Class)
		class = &value
	}
	return &api.AuthoringVerificationReport{
		AnswerPassed:      report.AnswerPassed,
		BuildPassed:       report.BuildPassed,
		CheckpointsPassed: report.CheckpointsPassed,
		Class:             class,
		Issues:            optionalSlice(issues),
		Summary:           optionalString(report.Summary),
	}
}

func toAPIAuthoringMessages(messages []authoring.Message) []api.AuthoringMessage {
	result := make([]api.AuthoringMessage, 0, len(messages))
	for _, message := range messages {
		changes := make([]api.AuthoringChange, 0, len(message.Changes))
		for _, change := range message.Changes {
			changes = append(changes, api.AuthoringChange{
				DifficultyImpact: change.DifficultyImpact,
				Kind:             change.Kind,
				Revision:         int(change.Revision),
				Summary:          change.Summary,
			})
		}
		result = append(result, api.AuthoringMessage{
			Changes:   optionalSlice(changes),
			Content:   message.Content,
			CreatedAt: message.CreatedAt.UTC(),
			Id:        message.ID,
			Role:      api.AuthoringMessageRole(message.Role),
		})
	}
	return result
}

func toAPIAuthoringAssets(assets []authoring.Asset) []api.AuthoringAsset {
	result := make([]api.AuthoringAsset, 0, len(assets))
	for _, asset := range assets {
		result = append(result, api.AuthoringAsset{Content: asset.Content, Path: asset.Path})
	}
	return result
}

func toAPIAuthoringFileDiffs(diff []authoring.FileDiff) []api.AuthoringFileDiff {
	result := make([]api.AuthoringFileDiff, 0, len(diff))
	for _, entry := range diff {
		result = append(result, api.AuthoringFileDiff{Diff: entry.Diff, Path: entry.Path})
	}
	return result
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func optionalSlice[T any](values []T) *[]T {
	if len(values) == 0 {
		return nil
	}
	return &values
}

func (h *Handler) syncAuthoringSession(ctx context.Context, sessionID string) error {
	session, err := h.db.GetAuthoringSessionInternal(ctx, sessionID)
	if err != nil {
		return err
	}
	if session.State == authoring.StatePublishing && session.PublishChallengeID != "" {
		// A filesystem promote completed but its DB completion was interrupted.
		// The catalog directory is already atomic, so finish the durable state.
		if published, err := challenge.Get(h.challengesDir, session.PublishChallengeID); err == nil {
			if err := h.db.CompletePublish(ctx, session.ID, session.UserID, session.VisibleRevision, session.PublishChallengeID); err != nil {
				return err
			}
			h.enqueuePublishedChallengeTaxonomy(ctx, *published)
			return nil
		}
	}
	if h.k8s == nil {
		return nil
	}
	if session.State == authoring.StateRevisingAndVerifying && strings.TrimSpace(session.GeneratorRunID) == "" {
		return h.startRevisedAuthoringGeneratorRun(ctx, session)
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
			return nil
		}
		artifact, err := h.storeVerifiedArtifact(ctx, session, task)
		if err != nil {
			return err
		}
		if err := h.db.CompleteGeneratorVerification(ctx, session.ID, session.GeneratorRunID, artifact, verification); err != nil {
			return err
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
	artifact := authoring.Artifact{
		SubmissionID:   task.Spec.Submission.ID,
		Directory:      authoring.ArtifactRelativePath(session.ID, session.CurrentRevision),
		GeneratorRunID: session.GeneratorRunID,
	}
	if _, err := os.Stat(target); err == nil {
		return artifact, nil
	} else if !os.IsNotExist(err) {
		return authoring.Artifact{}, fmt.Errorf("stat verified artifact: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return authoring.Artifact{}, fmt.Errorf("create verified artifact directory: %w", err)
	}
	temp, err := os.MkdirTemp(filepath.Dir(target), ".tmp-"+task.Spec.Submission.ID+"-")
	if err != nil {
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
	if err := os.Rename(temp, target); err != nil {
		// Concurrent polling may observe the same completed VerifyTask. Each
		// caller has an isolated staging directory; once one atomically promotes
		// it, every other caller can adopt the immutable target.
		if _, statErr := os.Stat(target); statErr == nil {
			return artifact, nil
		}
		return authoring.Artifact{}, fmt.Errorf("store verified artifact: %w", err)
	}
	return artifact, nil
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
