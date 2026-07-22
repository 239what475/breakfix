package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	breakfixv1 "github.com/breakfix/breakfix/apis/breakfix/v1"
	"github.com/breakfix/breakfix/internal/api"
	"github.com/breakfix/breakfix/internal/auth"
	"github.com/breakfix/breakfix/internal/challenge"
	"github.com/breakfix/breakfix/internal/config"
	"github.com/breakfix/breakfix/internal/db"
	"github.com/breakfix/breakfix/internal/draftreview"
	"github.com/breakfix/breakfix/internal/k8s"
	"github.com/gin-gonic/gin"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"log/slog"
)

type activeEnvironment struct {
	Runtime                string
	Name                   string
	ChallengeRef           string
	Namespace              string
	WorkspacePod           string
	Phase                  breakfixv1.EnvironmentPhase
	ExpiresAt              *metav1.Time
	SubmitResult           *breakfixv1.SubmitResult
	Message                string
	Reason                 string
	LastError              *breakfixv1.EnvironmentErrorStatus
	ReadyTimeoutSeconds    *int64
	IdleTTLSeconds         *int64
	DrainGraceSeconds      *int64
	DestroyTimeoutSeconds  *int64
	AutoDestroyAfterSubmit *bool
}

func environmentFromContainer(env *breakfixv1.ContainerEnvironment) *activeEnvironment {
	if env == nil {
		return nil
	}
	return &activeEnvironment{
		Runtime:                challenge.RuntimeContainer,
		Name:                   env.Name,
		ChallengeRef:           env.Spec.ChallengeRef,
		Namespace:              env.Status.Namespace,
		WorkspacePod:           env.Status.WorkspacePodName,
		Phase:                  env.Status.Phase,
		ExpiresAt:              env.Status.ExpiresAt,
		SubmitResult:           env.Status.SubmitResult,
		Message:                env.Status.Message,
		Reason:                 env.Status.Reason,
		LastError:              env.Status.LastError,
		ReadyTimeoutSeconds:    env.Spec.Timeouts.ReadyTimeoutSeconds,
		IdleTTLSeconds:         env.Spec.Timeouts.IdleTTLSeconds,
		DrainGraceSeconds:      env.Spec.Timeouts.DrainGracePeriodSeconds,
		DestroyTimeoutSeconds:  env.Spec.Timeouts.DestroyTimeoutSeconds,
		AutoDestroyAfterSubmit: env.Spec.CleanupPolicy.AutoDestroyAfterSubmit,
	}
}

func environmentFromVCluster(env *breakfixv1.VClusterEnvironment) *activeEnvironment {
	if env == nil {
		return nil
	}
	return &activeEnvironment{
		Runtime:                challenge.RuntimeVCluster,
		Name:                   env.Name,
		ChallengeRef:           env.Spec.ChallengeRef,
		Namespace:              env.Status.Namespace,
		WorkspacePod:           env.Status.WorkspacePodName,
		Phase:                  env.Status.Phase,
		ExpiresAt:              env.Status.ExpiresAt,
		SubmitResult:           env.Status.SubmitResult,
		Message:                env.Status.Message,
		Reason:                 env.Status.Reason,
		LastError:              env.Status.LastError,
		ReadyTimeoutSeconds:    env.Spec.Timeouts.ReadyTimeoutSeconds,
		IdleTTLSeconds:         env.Spec.Timeouts.IdleTTLSeconds,
		DrainGraceSeconds:      env.Spec.Timeouts.DrainGracePeriodSeconds,
		DestroyTimeoutSeconds:  env.Spec.Timeouts.DestroyTimeoutSeconds,
		AutoDestroyAfterSubmit: env.Spec.CleanupPolicy.AutoDestroyAfterSubmit,
	}
}

// Handler implements the OpenAPI-generated ServerInterface.
type Handler struct {
	db               *db.DB
	k8s              *k8s.Client
	reviewer         *draftreview.Reviewer
	registryAddr     string
	registryInsecure bool
	namespace        string
	crdNamespace     string
	challengesDir    string
	dataDir          string
	cooldownMin      int
	llm              config.LLMConfig
	jwtSecret        []byte
	internalAPIKey   string
	serverHost       string
	port             int
}

func NewHandler(database *db.DB, client *k8s.Client, cfg config.Config) *Handler {
	return &Handler{
		db:               database,
		k8s:              client,
		reviewer:         &draftreview.Reviewer{},
		registryAddr:     cfg.RegistryAddr,
		registryInsecure: cfg.RegistryInsecure,
		namespace:        cfg.Namespace,
		crdNamespace:     cfg.CRDNamespace,
		challengesDir:    cfg.ChallengesDir(),
		dataDir:          cfg.DataDir,
		cooldownMin:      cfg.CooldownMinutes,
		llm:              cfg.LLM,
		jwtSecret:        []byte(cfg.JWTSecret),
		internalAPIKey:   cfg.InternalAPIKey,
		serverHost:       cfg.ServerHost,
		port:             cfg.Port,
	}
}

// ── Auth ──

func (h *Handler) Register(c *gin.Context) {
	var req api.RegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: err.Error()})
		return
	}
	if len(req.Username) < 2 {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: "username too short"})
		return
	}
	if len(req.Password) < 6 {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: "password too short (min 6)"})
		return
	}
	if _, err := h.db.GetUserBySubject(req.Username); err == nil {
		c.JSON(http.StatusConflict, api.ErrorResponse{Error: "user already exists"})
		return
	}

	passwordHash, err := auth.HashPassword(req.Password)
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: "failed to hash password"})
		return
	}
	secret, url, err := auth.GenerateTOTPSecret(req.Username)
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: "failed to generate TOTP"})
		return
	}

	id := fmt.Sprintf("u-%d", time.Now().UnixNano())
	if _, err = h.db.CreateUserWithAuth(id, req.Username, passwordHash, secret); err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: "failed to create user"})
		return
	}

	slog.Info("user registered", "user", req.Username)
	c.JSON(http.StatusCreated, api.RegisterResponse{
		TotpSecret: &secret,
		TotpUrl:    &url,
	})
}

func (h *Handler) Login(c *gin.Context) {
	var req api.LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: err.Error()})
		return
	}

	user, err := h.db.GetUserBySubject(req.Username)
	if err != nil {
		c.JSON(http.StatusUnauthorized, api.ErrorResponse{Error: "invalid credentials"})
		return
	}
	if !auth.CheckPassword(user.PasswordHash, req.Password) {
		c.JSON(http.StatusUnauthorized, api.ErrorResponse{Error: "invalid credentials"})
		return
	}
	if !auth.ValidateTOTP(user.TOTPSecret, req.TotpCode) {
		c.JSON(http.StatusUnauthorized, api.ErrorResponse{Error: "invalid TOTP code"})
		return
	}

	token, err := auth.GenerateJWT(user.ID, user.Name, h.jwtSecret)
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: "failed to generate token"})
		return
	}

	slog.Info("user logged in", "user", req.Username)
	c.JSON(http.StatusOK, api.LoginResponse{
		Token:  &token,
		UserId: &user.ID,
		Name:   &user.Name,
	})
}

// ── Challenges ──

func (h *Handler) ListChallenges(c *gin.Context) {
	user := h.getUser(c)

	challenges, err := challenge.List(h.challengesDir)
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: err.Error()})
		return
	}

	solved := make(map[string]bool)
	active := make(map[string]bool)
	if user != nil {
		envs, err := h.listActiveEnvironments(c.Request.Context(), user.ID)
		if err == nil {
			for _, env := range envs {
				if env.SubmitResult != nil && env.SubmitResult.Passed {
					solved[env.ChallengeRef] = true
				}
				if env.Phase == breakfixv1.EnvironmentReady || env.Phase == breakfixv1.EnvironmentDraining {
					active[env.ChallengeRef] = true
				}
			}
		}
	}

	var summaries []api.ChallengeSummary
	for _, ch := range challenges {
		solvedVal := solved[ch.ID]
		activeVal := active[ch.ID]
		s := api.ChallengeSummary{
			Id:          &ch.ID,
			Title:       &ch.Title,
			Type:        &ch.Type,
			Runtime:     &ch.Runtime,
			Difficulty:  &ch.Difficulty,
			Description: &ch.Description,
			Solved:      &solvedVal,
			Active:      &activeVal,
		}
		tags := append([]string{}, ch.Tags...)
		s.Tags = &tags
		summaries = append(summaries, s)
	}
	c.JSON(http.StatusOK, api.ChallengeList{Challenges: &summaries})
}

// ── Environments ──

func (h *Handler) StartChallenge(c *gin.Context, id string) {
	user := h.requireUser(c)
	if user == nil {
		return
	}

	challengeEntry, err := challenge.Get(h.challengesDir, id)
	if err != nil {
		c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "challenge not found"})
		return
	}

	existing, _ := h.findEnvironment(c.Request.Context(), user.ID, challengeEntry)
	if existing != nil {
		if existing.Phase == breakfixv1.EnvironmentDraining {
			if err := h.resumeEnvironment(c.Request.Context(), existing); err != nil {
				c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("resume environment: %v", err)})
				return
			}
		}
		slog.Info("resuming existing environment", "environment", existing.Name, "challenge", id, "runtime", existing.Runtime)
		title := challengeEntry.Title
		c.JSON(http.StatusOK, api.StartResponse{ChallengeTitle: &title})
		return
	}

	env, err := h.createEnvironment(c.Request.Context(), user, challengeEntry)
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: err.Error()})
		return
	}

	slog.Info("environment started", "environment", env.Name, "user", user.ID, "challenge", challengeEntry.ID, "runtime", challengeEntry.Runtime)
	title := challengeEntry.Title
	c.JSON(http.StatusOK, api.StartResponse{ChallengeTitle: &title})
}

func (h *Handler) SubmitChallenge(c *gin.Context, id string) {
	user := h.requireUser(c)
	if user == nil {
		return
	}

	challengeEntry, err := challenge.Get(h.challengesDir, id)
	if err != nil {
		c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "challenge not found"})
		return
	}

	env, err := h.findEnvironment(c.Request.Context(), user.ID, challengeEntry)
	if err != nil {
		c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "no active environment for this challenge"})
		return
	}
	if env.Phase != breakfixv1.EnvironmentReady && env.Phase != breakfixv1.EnvironmentDraining {
		c.JSON(http.StatusConflict, api.ErrorResponse{Error: "environment not ready"})
		return
	}

	if err := h.submitEnvironment(c.Request.Context(), env); err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("update environment: %v", err)})
		return
	}

	result, err := h.waitSubmitResult(c.Request.Context(), env.Runtime, env.Name, 30*time.Second)
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("submit failed: %v", err)})
		return
	}

	exitCode := int(result.ExitCode)
	if environmentAutoDestroyAfterSubmit(env) {
		if err := h.destroyEnvironment(c.Request.Context(), env); err != nil {
			slog.Warn("failed to cleanup submitted environment", "environment", env.Name, "err", err)
		}
	}
	c.JSON(http.StatusOK, api.SubmitResponse{
		Passed:   &result.Passed,
		ExitCode: &exitCode,
		Output:   &result.Output,
	})
}

func (h *Handler) ResetChallenge(c *gin.Context, id string) {
	user := h.requireUser(c)
	if user == nil {
		return
	}

	challengeEntry, err := challenge.Get(h.challengesDir, id)
	if err != nil {
		c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "challenge not found"})
		return
	}

	existing, _ := h.findEnvironment(c.Request.Context(), user.ID, challengeEntry)
	if existing != nil {
		if err := h.destroyEnvironment(c.Request.Context(), existing); err != nil {
			slog.Error("failed to destroy old environment", "err", err)
		}
		h.waitDestroyed(c.Request.Context(), existing.Runtime, existing.Name)
		slog.Info("old environment destroyed", "environment", existing.Name, "runtime", existing.Runtime)
	}

	env, err := h.createEnvironment(c.Request.Context(), user, challengeEntry)
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: err.Error()})
		return
	}

	slog.Info("environment reset", "environment", env.Name, "user", user.ID, "challenge", challengeEntry.ID)
	title := challengeEntry.Title
	c.JSON(http.StatusOK, api.ResetResponse{ChallengeTitle: &title})
}

func (h *Handler) StopChallenge(c *gin.Context, id string) {
	user := h.requireUser(c)
	if user == nil {
		return
	}

	challengeEntry, err := challenge.Get(h.challengesDir, id)
	if err != nil {
		c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "challenge not found"})
		return
	}

	env, err := h.findEnvironment(c.Request.Context(), user.ID, challengeEntry)
	if err != nil {
		c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "no active environment for this challenge"})
		return
	}

	if err := h.destroyEnvironment(c.Request.Context(), env); err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("stop environment: %v", err)})
		return
	}
	h.waitDestroyed(c.Request.Context(), env.Runtime, env.Name)

	stopped := true
	title := challengeEntry.Title
	c.JSON(http.StatusOK, api.StopResponse{
		Stopped:        &stopped,
		ChallengeTitle: &title,
	})
}

// ── Agent ──

func (h *Handler) ReviewGenerationDraft(c *gin.Context) {
	var req api.GenerateDraftRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: err.Error()})
		return
	}

	result, err := h.reviewer.Review(c.Request.Context(), req.Topic)
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("review draft: %v", err)})
		return
	}

	status := "success"
	verdict := result.Verdict
	reason := result.Reason
	draft := toAPIChallengeDraft(result.Draft)
	c.JSON(http.StatusOK, api.GenerateDraftResponse{
		Status:   &status,
		Verdict:  &verdict,
		Reason:   &reason,
		Warnings: &result.Warnings,
		Draft:    &draft,
	})
}

func (h *Handler) CreateGenerationJob(c *gin.Context) {
	if err := h.checkRegistryReady(c.Request.Context()); err != nil {
		c.JSON(http.StatusServiceUnavailable, api.ErrorResponse{Error: err.Error()})
		return
	}

	var req api.GenerationJobCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: err.Error()})
		return
	}

	draft := fromAPIChallengeDraft(req.Draft)
	genID := "gen-" + k8s.RandomID()

	env := map[string]string{
		"CHALLENGE_DRAFT_JSON":           mustJSON(draft),
		"CHALLENGE_OUTPUT_DIR":           "/workspace/out",
		"REGISTRY_ADDR":                  h.registryAddr,
		"LAB_NAMESPACE":                  h.crdNamespace,
		"GATEWAY_INTERNAL_URL":           h.internalGatewayURL(),
		"GATEWAY_INTERNAL_API_KEY":       h.internalAPIKey,
		"GENERATION_ID":                  genID,
		"ANTHROPIC_BASE_URL":             h.llm.BaseURL,
		"ANTHROPIC_AUTH_TOKEN":           h.llm.APIKey,
		"ANTHROPIC_MODEL":                h.llm.Model,
		"ANTHROPIC_DEFAULT_OPUS_MODEL":   h.llm.Model,
		"ANTHROPIC_DEFAULT_SONNET_MODEL": h.llm.Model,
		"ANTHROPIC_DEFAULT_HAIKU_MODEL":  h.llm.HaikuModel,
		"CLAUDE_CODE_SUBAGENT_MODEL":     h.llm.HaikuModel,
		"CLAUDE_CODE_EFFORT_LEVEL":       h.llm.Effort,
	}
	if authz := strings.TrimSpace(c.GetHeader("Authorization")); authz != "" {
		env["BREAKFIX_GENERATION_JWT"] = strings.TrimPrefix(authz, "Bearer ")
	}
	if h.registryInsecure {
		env["REGISTRY_INSECURE"] = "true"
	}

	gen := &breakfixv1.Generation{
		ObjectMeta: metav1.ObjectMeta{
			Name:      genID,
			Namespace: h.crdNamespace,
		},
		Spec: breakfixv1.GenerationSpec{
			Draft: &draft,
			Image: h.generatorImage(),
			Env:   env,
		},
	}

	if _, err := h.k8s.CreateGeneration(c.Request.Context(), h.crdNamespace, gen); err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("create generation: %v", err)})
		return
	}

	slog.Info("generation created", "generation", genID, "title", draft.Title)
	status := "queued"
	jobID := genID
	message := "generation job created"
	c.JSON(http.StatusOK, api.GenerationJobResponse{
		JobId:   &jobID,
		Status:  &status,
		Message: &message,
	})
}

func (h *Handler) GetGenerationJob(c *gin.Context, id string) {
	gen, err := h.k8s.GetGeneration(c.Request.Context(), h.crdNamespace, id)
	if err != nil {
		c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "generation job not found"})
		return
	}

	status := string(gen.Status.Phase)
	if status == "" {
		status = "Pending"
	}
	status = normalizeGenerationStatus(status)
	message := gen.Status.Message
	if message == "" {
		message = defaultGenerationMessage(gen)
	}

	var challengeID *string
	if gen.Status.Challenge != nil {
		cid := gen.Status.Challenge.ID
		challengeID = &cid
	}

	var startedAt *time.Time
	if gen.Status.StartedAt != nil {
		t := gen.Status.StartedAt.Time
		startedAt = &t
	}
	var completedAt *time.Time
	if gen.Status.CompletedAt != nil {
		t := gen.Status.CompletedAt.Time
		completedAt = &t
	}

	jobID := gen.Name
	c.JSON(http.StatusOK, api.GenerationJobResponse{
		JobId:       &jobID,
		ChallengeId: challengeID,
		Status:      &status,
		Message:     &message,
		StartedAt:   startedAt,
		CompletedAt: completedAt,
	})
}

func (h *Handler) CreateVerifySubmission(c *gin.Context) {
	user := h.requireUser(c)
	if user == nil {
		return
	}
	if err := h.checkRegistryReady(c.Request.Context()); err != nil {
		c.JSON(http.StatusServiceUnavailable, api.ErrorResponse{Error: err.Error()})
		return
	}

	file, _, err := c.Request.FormFile("artifact")
	if err != nil {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: "artifact file is required"})
		return
	}
	defer file.Close()

	submissionID := "sub-" + k8s.RandomID()
	challengeID := challenge.NewID()
	if _, err := challenge.SaveSubmission(h.dataDir, submissionID, file); err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("save submission: %v", err)})
		return
	}

	verifyTaskID := "vt-" + k8s.RandomID()
	task := &breakfixv1.VerifyTask{
		ObjectMeta: metav1.ObjectMeta{
			Name:      verifyTaskID,
			Namespace: h.crdNamespace,
		},
		Spec: breakfixv1.VerifyTaskSpec{
			Source: breakfixv1.VerifyTaskSource{
				Kind: "user",
				Ref:  user.ID,
			},
			ChallengeID: challengeID,
			Submission: breakfixv1.VerifyTaskSubmission{
				ID: submissionID,
			},
		},
		Status: breakfixv1.VerifyTaskStatus{
			Phase:   breakfixv1.VerifyTaskPending,
			Message: "verification task accepted",
		},
	}
	if _, err := h.k8s.CreateVerifyTask(c.Request.Context(), h.crdNamespace, task); err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("create verify task: %v", err)})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"challenge_id":   challengeID,
		"verify_task_id": verifyTaskID,
		"submission_id":  submissionID,
		"status":         "queued",
	})
}

func (h *Handler) GetVerifyTask(c *gin.Context, id string) {
	task, err := h.k8s.GetVerifyTask(c.Request.Context(), h.crdNamespace, id)
	if err != nil {
		c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "verify task not found"})
		return
	}

	status := normalizeVerifyTaskStatus(task.Status.Phase)
	message := task.Status.Message
	if strings.TrimSpace(message) == "" {
		message = defaultVerifyTaskMessage(task)
	}

	resp := api.VerifyTaskResponse{
		ChallengeId:  &task.Spec.ChallengeID,
		VerifyTaskId: &task.Name,
		SubmissionId: &task.Spec.Submission.ID,
		Status:       &status,
		Message:      &message,
	}
	if task.Status.StartedAt != nil {
		t := task.Status.StartedAt.Time
		resp.StartedAt = &t
	}
	if task.Status.CompletedAt != nil {
		t := task.Status.CompletedAt.Time
		resp.CompletedAt = &t
	}
	if task.Status.Report != nil {
		report := toAPIVerifyReport(task.Status.Report)
		resp.Report = &report
	}

	c.JSON(http.StatusOK, resp)
}

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

	file, _, err := c.Request.FormFile("artifact")
	if err != nil {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: "artifact file is required"})
		return
	}
	defer file.Close()

	submissionID := "sub-" + k8s.RandomID()
	challengeID := challenge.NewID()
	if _, err := challenge.SaveSubmission(h.dataDir, submissionID, file); err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("save submission: %v", err)})
		return
	}

	verifyTaskID := "vt-" + k8s.RandomID()
	task := &breakfixv1.VerifyTask{
		ObjectMeta: metav1.ObjectMeta{
			Name:      verifyTaskID,
			Namespace: h.crdNamespace,
		},
		Spec: breakfixv1.VerifyTaskSpec{
			Source: breakfixv1.VerifyTaskSource{
				Kind: "agent",
				Ref:  genID,
			},
			ChallengeID: challengeID,
			Submission: breakfixv1.VerifyTaskSubmission{
				ID: submissionID,
			},
		},
		Status: breakfixv1.VerifyTaskStatus{
			Phase:   breakfixv1.VerifyTaskPending,
			Message: "verification task accepted",
		},
	}
	if _, err := h.k8s.CreateVerifyTask(c.Request.Context(), h.crdNamespace, task); err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("create verify task: %v", err)})
		return
	}

	gen.Status.VerifyTaskRef = verifyTaskID
	gen.Status.Message = "artifact submitted for verification"
	if _, err := h.k8s.UpdateGenerationStatus(c.Request.Context(), h.crdNamespace, gen); err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("update generation status: %v", err)})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"challenge_id":   challengeID,
		"status":         "ok",
		"submission_id":  submissionID,
		"verify_task_id": verifyTaskID,
	})
}

// ── Terminal (WebSocket) ──

func (h *Handler) HandleTerminal(c *gin.Context) {
	challengeID := c.Param("id")
	user := h.requireUser(c)
	if user == nil {
		return
	}

	challengeEntry, err := challenge.Get(h.challengesDir, challengeID)
	if err != nil {
		c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "challenge not found"})
		return
	}

	env, err := h.findEnvironment(c.Request.Context(), user.ID, challengeEntry)
	if err != nil {
		c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "no active environment for this challenge"})
		return
	}
	if env.WorkspacePod == "" {
		c.JSON(http.StatusServiceUnavailable, api.ErrorResponse{Error: "pod not ready"})
		return
	}

	runtimeAdapter, err := h.environmentRuntimeAdapter(env.Runtime)
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: err.Error()})
		return
	}

	slog.Info("terminal session started", "challenge", challengeID, "user", user.ID)
	wsUpgrade(c.Writer, c.Request, env, h.k8s, runtimeAdapter, h.cooldownMin)
}

func (h *Handler) DownloadVerifySubmissionArtifact(c *gin.Context) {
	if h.internalAPIKey == "" {
		c.JSON(http.StatusServiceUnavailable, api.ErrorResponse{Error: "internal download disabled"})
		return
	}
	if c.GetHeader("X-Breakfix-Internal-Key") != h.internalAPIKey {
		c.JSON(http.StatusUnauthorized, api.ErrorResponse{Error: "invalid internal key"})
		return
	}

	submissionID := strings.TrimSpace(c.Param("id"))
	if submissionID == "" {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: "missing submission id"})
		return
	}
	path := challenge.SubmissionPath(h.dataDir, submissionID)
	f, err := os.Open(path)
	if err != nil {
		c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "submission artifact not found"})
		return
	}
	defer f.Close()

	c.Header("Content-Type", "application/gzip")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%s.tar.gz", submissionID))
	c.Status(http.StatusOK)
	_, _ = io.Copy(c.Writer, f)
}

// ── Auth helpers ──

func (h *Handler) getUser(c *gin.Context) *db.User {
	uid, exists := c.Get("user_id")
	if !exists {
		return nil
	}
	user, err := h.db.GetUserByID(uid.(string))
	if err != nil {
		return nil
	}
	return user
}

func (h *Handler) requireUser(c *gin.Context) *db.User {
	user := h.getUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, api.ErrorResponse{Error: "login required"})
		return nil
	}
	return user
}

func (h *Handler) generatorImage() string {
	if h.registryAddr == "" {
		return "breakfix-generator:latest"
	}
	return h.registryAddr + "/breakfix-generator:latest"
}

// ── Environment helpers ──

func (h *Handler) listActiveEnvironments(ctx context.Context, userID string) ([]activeEnvironment, error) {
	selector := fmt.Sprintf("breakfix.dev/user=%s", userID)
	result := make([]activeEnvironment, 0, 4)
	for _, runtime := range []string{challenge.RuntimeContainer, challenge.RuntimeVCluster} {
		adapter, err := h.environmentRuntimeAdapter(runtime)
		if err != nil {
			return nil, err
		}
		envs, err := adapter.list(ctx, selector)
		if err != nil {
			return nil, err
		}
		result = append(result, envs...)
	}
	return result, nil
}

func (h *Handler) findEnvironment(ctx context.Context, userID string, challengeEntry *challenge.Entry) (*activeEnvironment, error) {
	selector := fmt.Sprintf("breakfix.dev/user=%s,breakfix.dev/challenge=%s", userID, challengeEntry.ID)
	adapter, err := h.environmentRuntimeAdapter(challengeEntry.Runtime)
	if err != nil {
		return nil, err
	}
	envs, err := adapter.list(ctx, selector)
	if err != nil {
		return nil, err
	}
	for i := range envs {
		env := envs[i]
		if isLiveEnvironmentPhase(env.Phase) {
			return &env, nil
		}
	}
	return nil, errors.New("no active environment")
}

func (h *Handler) createEnvironment(ctx context.Context, user *db.User, challengeEntry *challenge.Entry) (*activeEnvironment, error) {
	adapter, err := h.environmentRuntimeAdapter(challengeEntry.Runtime)
	if err != nil {
		return nil, err
	}
	name, err := adapter.create(ctx, user, challengeEntry)
	if err != nil {
		return nil, err
	}
	return h.waitEnvironmentReady(ctx, adapter.runtime, name, time.Duration(adapter.readyTimeoutDuration())*time.Second)
}

func (h *Handler) resumeEnvironment(ctx context.Context, env *activeEnvironment) error {
	adapter, err := h.environmentRuntimeAdapter(env.Runtime)
	if err != nil {
		return err
	}
	return adapter.updateSessionStatus(ctx, env.Name, func(spec *breakfixv1.CommonEnvironmentSpec, status *breakfixv1.CommonEnvironmentStatus) {
		expiresAt := metav1.NewTime(time.Now().Add(spec.IdleTTLOr(time.Duration(h.cooldownMin) * time.Minute)))
		setGatewayEnvironmentReady(status, expiresAt, "SessionResumed", "environment resumed")
	})
}

func (h *Handler) submitEnvironment(ctx context.Context, env *activeEnvironment) error {
	adapter, err := h.environmentRuntimeAdapter(env.Runtime)
	if err != nil {
		return err
	}
	return adapter.updateSpec(ctx, env.Name, func(spec *breakfixv1.CommonEnvironmentSpec) {
		spec.Submit = true
	})
}

func (h *Handler) destroyEnvironment(ctx context.Context, env *activeEnvironment) error {
	adapter, err := h.environmentRuntimeAdapter(env.Runtime)
	if err != nil {
		return err
	}
	if adapter.requestDeletion == nil {
		return fmt.Errorf("runtime %q does not support deletion", env.Runtime)
	}
	return adapter.requestDeletion(ctx, env.Name)
}

func (h *Handler) waitEnvironmentReady(ctx context.Context, runtime, name string, timeout time.Duration) (*activeEnvironment, error) {
	effectiveTimeout := timeout
	deadline := time.Now().Add(effectiveTimeout)
	for time.Now().Before(deadline) {
		env, err := h.getEnvironment(ctx, runtime, name)
		if err != nil {
			return nil, err
		}
		if readyTimeout := environmentReadyTimeout(env, effectiveTimeout); readyTimeout > 0 && readyTimeout != effectiveTimeout {
			effectiveTimeout = readyTimeout
			deadline = time.Now().Add(effectiveTimeout)
		}
		if env.Phase == breakfixv1.EnvironmentReady {
			return env, nil
		}
		if env.Phase == breakfixv1.EnvironmentDestroyed || env.Phase == breakfixv1.EnvironmentFailed {
			return nil, environmentUnavailableError(env)
		}
		time.Sleep(500 * time.Millisecond)
	}
	return nil, fmt.Errorf("timeout waiting for environment ready")
}

func (h *Handler) getEnvironment(ctx context.Context, runtime, name string) (*activeEnvironment, error) {
	adapter, err := h.environmentRuntimeAdapter(runtime)
	if err != nil {
		return nil, err
	}
	return adapter.get(ctx, name)
}

func (h *Handler) waitSubmitResult(ctx context.Context, runtime, name string, timeout time.Duration) (*breakfixv1.SubmitResult, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		env, err := h.getEnvironment(ctx, runtime, name)
		if err != nil {
			return nil, err
		}
		if env.SubmitResult != nil {
			return env.SubmitResult, nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return nil, fmt.Errorf("timeout waiting for submit result")
}

func (h *Handler) waitDestroyed(ctx context.Context, runtime, name string) {
	effectiveTimeout := 30 * time.Second
	if env, err := h.getEnvironment(ctx, runtime, name); err == nil {
		if timeout := environmentDestroyTimeout(env, effectiveTimeout); timeout > 0 {
			effectiveTimeout = timeout
		}
	}
	deadline := time.Now().Add(effectiveTimeout)
	for time.Now().Before(deadline) {
		env, err := h.getEnvironment(ctx, runtime, name)
		if err != nil {
			return
		}
		if env.Phase == breakfixv1.EnvironmentDestroyed {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func isLiveEnvironmentPhase(phase breakfixv1.EnvironmentPhase) bool {
	return phase == "" ||
		phase == breakfixv1.EnvironmentPending ||
		phase == breakfixv1.EnvironmentProvisioning ||
		phase == breakfixv1.EnvironmentReady ||
		phase == breakfixv1.EnvironmentDraining
}

func environmentReadyTimeout(env *activeEnvironment, fallback time.Duration) time.Duration {
	if env == nil || env.ReadyTimeoutSeconds == nil || *env.ReadyTimeoutSeconds <= 0 {
		return fallback
	}
	return time.Duration(*env.ReadyTimeoutSeconds) * time.Second
}

func environmentIdleTTL(env *activeEnvironment, fallback time.Duration) time.Duration {
	if env == nil || env.IdleTTLSeconds == nil || *env.IdleTTLSeconds <= 0 {
		return fallback
	}
	return time.Duration(*env.IdleTTLSeconds) * time.Second
}

func environmentDrainGracePeriod(env *activeEnvironment, fallback time.Duration) time.Duration {
	if env == nil || env.DrainGraceSeconds == nil || *env.DrainGraceSeconds <= 0 {
		return fallback
	}
	return time.Duration(*env.DrainGraceSeconds) * time.Second
}

func environmentDestroyTimeout(env *activeEnvironment, fallback time.Duration) time.Duration {
	if env == nil || env.DestroyTimeoutSeconds == nil || *env.DestroyTimeoutSeconds <= 0 {
		return fallback
	}
	return time.Duration(*env.DestroyTimeoutSeconds) * time.Second
}

func environmentAutoDestroyAfterSubmit(env *activeEnvironment) bool {
	return env == nil || env.AutoDestroyAfterSubmit == nil || *env.AutoDestroyAfterSubmit
}

func environmentUnavailableError(env *activeEnvironment) error {
	if env == nil {
		return errors.New("environment became unavailable before ready")
	}
	if env.LastError != nil && strings.TrimSpace(env.LastError.Message) != "" {
		return errors.New(env.LastError.Message)
	}
	if strings.TrimSpace(env.Message) != "" {
		return errors.New(env.Message)
	}
	if strings.TrimSpace(env.Reason) != "" {
		return fmt.Errorf("environment became unavailable before ready: %s", env.Reason)
	}
	return errors.New("environment became unavailable before ready")
}

func toAPIChallengeDraft(d breakfixv1.ChallengeDraft) api.ChallengeDraft {
	tags := append([]string{}, d.Tags...)
	return api.ChallengeDraft{
		Title:              d.Title,
		Difficulty:         d.Difficulty,
		Tags:               tags,
		Description:        d.Description,
		Goal:               d.Goal,
		Symptoms:           d.Symptoms,
		FaultMechanism:     d.FaultMechanism,
		EnvironmentShape:   d.EnvironmentShape,
		AcceptanceCriteria: d.AcceptanceCriteria,
		DifficultyReason:   d.DifficultyReason,
		Notes:              &d.Notes,
	}
}

func fromAPIChallengeDraft(d api.ChallengeDraft) breakfixv1.ChallengeDraft {
	tags := append([]string{}, d.Tags...)
	notes := ""
	if d.Notes != nil {
		notes = *d.Notes
	}
	return breakfixv1.ChallengeDraft{
		Title:              d.Title,
		Difficulty:         d.Difficulty,
		Tags:               tags,
		Description:        d.Description,
		Goal:               d.Goal,
		Symptoms:           d.Symptoms,
		FaultMechanism:     d.FaultMechanism,
		EnvironmentShape:   d.EnvironmentShape,
		AcceptanceCriteria: d.AcceptanceCriteria,
		DifficultyReason:   d.DifficultyReason,
		Notes:              notes,
	}
}

func mustJSON(v any) string {
	data, _ := json.Marshal(v)
	return string(data)
}

func normalizeGenerationStatus(status string) string {
	switch status {
	case "Pending":
		return "queued"
	case "Running":
		return "running"
	case "Succeeded":
		return "success"
	case "Failed":
		return "failed"
	default:
		return strings.ToLower(status)
	}
}

func normalizeVerifyTaskStatus(status breakfixv1.VerifyTaskPhase) string {
	switch status {
	case breakfixv1.VerifyTaskPending:
		return "queued"
	case breakfixv1.VerifyTaskRunning:
		return "running"
	case breakfixv1.VerifyTaskVerified:
		return "publishing"
	case breakfixv1.VerifyTaskSucceeded:
		return "success"
	case breakfixv1.VerifyTaskFailed:
		return "failed"
	default:
		return strings.ToLower(string(status))
	}
}

func defaultGenerationMessage(gen *breakfixv1.Generation) string {
	switch gen.Status.Phase {
	case breakfixv1.GenerationPending:
		return "generation request accepted"
	case breakfixv1.GenerationRunning:
		if strings.TrimSpace(gen.Status.VerifyTaskRef) != "" {
			return "artifact submitted, verification running"
		}
		return "generating challenge files"
	case breakfixv1.GenerationSucceeded:
		if gen.Status.Challenge != nil {
			return fmt.Sprintf("challenge %s generated", gen.Status.Challenge.ID)
		}
		return "challenge generated"
	case breakfixv1.GenerationFailed:
		return "generation failed"
	default:
		return "generation status updated"
	}
}

func defaultVerifyTaskMessage(task *breakfixv1.VerifyTask) string {
	switch task.Status.Phase {
	case breakfixv1.VerifyTaskPending:
		return "verification task accepted"
	case breakfixv1.VerifyTaskRunning:
		return "verification running"
	case breakfixv1.VerifyTaskVerified:
		return "verification passed, publishing challenge"
	case breakfixv1.VerifyTaskSucceeded:
		return "verification passed and published"
	case breakfixv1.VerifyTaskFailed:
		return "verification failed"
	default:
		return "verification status updated"
	}
}

func toAPIVerifyReport(report *breakfixv1.VerifyReport) api.VerifyReport {
	out := api.VerifyReport{}
	out.BuildPassed = &report.BuildPassed
	out.AnswerPassed = &report.AnswerPassed
	out.VerifyPassed = &report.VerifyPassed
	if strings.TrimSpace(report.Summary) != "" {
		out.Summary = &report.Summary
	}
	if len(report.Issues) > 0 {
		issues := make([]api.VerifyIssue, 0, len(report.Issues))
		for _, issue := range report.Issues {
			code := issue.Code
			message := issue.Message
			issues = append(issues, api.VerifyIssue{
				Code:    &code,
				Message: &message,
			})
		}
		out.Issues = &issues
	}
	return out
}

func (h *Handler) checkRegistryReady(ctx context.Context) error {
	addr := strings.TrimSpace(h.registryAddr)
	if addr == "" {
		return fmt.Errorf("registry is not configured")
	}
	registry := addr
	if idx := strings.IndexByte(registry, '/'); idx >= 0 {
		registry = registry[:idx]
	}
	url := "http://" + registry + "/v2/"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build registry health request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("registry %s is unavailable: %w", registry, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusUnauthorized {
		return fmt.Errorf("registry %s health check failed: status %d", registry, resp.StatusCode)
	}
	return nil
}

func (h *Handler) internalGatewayURL() string {
	host := strings.TrimSpace(h.serverHost)
	if host == "" {
		host = "172.18.0.1"
	}
	addr := net.JoinHostPort(host, fmt.Sprintf("%d", h.port))
	return "http://" + addr
}
