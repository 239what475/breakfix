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
	"github.com/breakfix/breakfix/internal/authoring"
	"github.com/breakfix/breakfix/internal/challenge"
	"github.com/breakfix/breakfix/internal/config"
	"github.com/breakfix/breakfix/internal/db"
	"github.com/breakfix/breakfix/internal/k8s"
	"github.com/gin-gonic/gin"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"log/slog"
)

type activeEnvironment struct {
	Runtime               string
	Name                  string
	ChallengeRef          string
	Namespace             string
	WorkspacePod          string
	Phase                 breakfixv1.EnvironmentPhase
	ExpiresAt             *metav1.Time
	Checkpoints           *breakfixv1.CheckpointStatus
	Message               string
	Reason                string
	LastError             *breakfixv1.EnvironmentErrorStatus
	ReadyTimeoutSeconds   *int64
	IdleTTLSeconds        *int64
	DrainGraceSeconds     *int64
	DestroyTimeoutSeconds *int64
}

func environmentFromContainer(env *breakfixv1.ContainerEnvironment) *activeEnvironment {
	if env == nil {
		return nil
	}
	return &activeEnvironment{
		Runtime:               challenge.RuntimeContainer,
		Name:                  env.Name,
		ChallengeRef:          env.Spec.ChallengeRef,
		Namespace:             env.Status.Namespace,
		WorkspacePod:          env.Status.WorkspacePodName,
		Phase:                 env.Status.Phase,
		ExpiresAt:             env.Status.ExpiresAt,
		Checkpoints:           env.Status.Checkpoints,
		Message:               env.Status.Message,
		Reason:                env.Status.Reason,
		LastError:             env.Status.LastError,
		ReadyTimeoutSeconds:   env.Spec.Timeouts.ReadyTimeoutSeconds,
		IdleTTLSeconds:        env.Spec.Timeouts.IdleTTLSeconds,
		DrainGraceSeconds:     env.Spec.Timeouts.DrainGracePeriodSeconds,
		DestroyTimeoutSeconds: env.Spec.Timeouts.DestroyTimeoutSeconds,
	}
}

func environmentFromVCluster(env *breakfixv1.VClusterEnvironment) *activeEnvironment {
	if env == nil {
		return nil
	}
	return &activeEnvironment{
		Runtime:               challenge.RuntimeVCluster,
		Name:                  env.Name,
		ChallengeRef:          env.Spec.ChallengeRef,
		Namespace:             env.Status.Namespace,
		WorkspacePod:          env.Status.WorkspacePodName,
		Phase:                 env.Status.Phase,
		ExpiresAt:             env.Status.ExpiresAt,
		Checkpoints:           env.Status.Checkpoints,
		Message:               env.Status.Message,
		Reason:                env.Status.Reason,
		LastError:             env.Status.LastError,
		ReadyTimeoutSeconds:   env.Spec.Timeouts.ReadyTimeoutSeconds,
		IdleTTLSeconds:        env.Spec.Timeouts.IdleTTLSeconds,
		DrainGraceSeconds:     env.Spec.Timeouts.DrainGracePeriodSeconds,
		DestroyTimeoutSeconds: env.Spec.Timeouts.DestroyTimeoutSeconds,
	}
}

// Handler implements the OpenAPI-generated ServerInterface.
type Handler struct {
	db               *db.DB
	k8s              *k8s.Client
	authoring        *authoring.Service
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
	terminals        *terminalConnectionTracker
}

func NewHandler(database *db.DB, client *k8s.Client, cfg config.Config) *Handler {
	return &Handler{
		db:               database,
		k8s:              client,
		authoring:        authoring.NewService(database, cfg.LLM),
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
		terminals:        newTerminalConnectionTracker(time.Second),
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
				if env.Phase == breakfixv1.EnvironmentCompleted {
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
			Runtime:     challengeSummaryRuntime(ch.Runtime),
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

func (h *Handler) GetChallengeContent(c *gin.Context, id string) {
	if h.requireUser(c) == nil {
		return
	}
	entry, err := challenge.Get(h.challengesDir, id)
	if err != nil {
		c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "challenge not found"})
		return
	}
	content, err := challenge.ReadContent(entry)
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: err.Error()})
		return
	}
	checkpoints := toAPICheckpoints(entry.Checkpoints)
	hints := content.Hints
	c.JSON(http.StatusOK, api.ChallengeContent{
		Id:          &entry.ID,
		Title:       &entry.Title,
		Problem:     &content.Problem,
		Solution:    &content.Solution,
		Hints:       &hints,
		Checkpoints: &checkpoints,
	})
}

func (h *Handler) GetChallengeProgress(c *gin.Context, id string) {
	user := h.requireUser(c)
	if user == nil {
		return
	}
	entry, err := challenge.Get(h.challengesDir, id)
	if err != nil {
		c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "challenge not found"})
		return
	}
	env, err := h.findProgressEnvironment(c.Request.Context(), user.ID, entry)
	if err != nil {
		c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "no active environment for this challenge"})
		return
	}
	if env.Phase != breakfixv1.EnvironmentReady && env.Phase != breakfixv1.EnvironmentDraining && env.Phase != breakfixv1.EnvironmentCompleted {
		c.JSON(http.StatusConflict, api.ErrorResponse{Error: "environment is not ready for checkpoint checks"})
		return
	}
	if env.Checkpoints == nil {
		checks := []api.CheckpointResult{}
		c.JSON(http.StatusOK, api.ChallengeProgress{Checks: &checks})
		return
	}
	if env.Checkpoints.Error != "" {
		c.JSON(http.StatusUnprocessableEntity, api.ErrorResponse{Error: env.Checkpoints.Error})
		return
	}
	checks := toAPICheckStatusResults(env.Checkpoints.Results)
	c.JSON(http.StatusOK, api.ChallengeProgress{Checks: &checks})
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

	existing, _ := h.findProgressEnvironment(c.Request.Context(), user.ID, challengeEntry)
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

	env, err := h.findProgressEnvironment(c.Request.Context(), user.ID, challengeEntry)
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
	windowName, err := parseTerminalWindow(c.Query("window"))
	if err != nil {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: err.Error()})
		return
	}

	runtimeAdapter, err := h.environmentRuntimeAdapter(env.Runtime)
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: err.Error()})
		return
	}
	if env.Phase == breakfixv1.EnvironmentDraining {
		if err := h.resumeEnvironment(c.Request.Context(), env); err != nil {
			c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("resume environment: %v", err)})
			return
		}
		env.Phase = breakfixv1.EnvironmentReady
	}

	slog.Info("terminal session started", "challenge", challengeID, "user", user.ID)
	key := env.Runtime + "/" + env.Name
	wsUpgrade(c.Writer, c.Request, env, h.k8s, runtimeAdapter, h.cooldownMin, windowName,
		func() { h.terminals.open(key) },
		func() {
			h.terminals.close(key, func() {
				expiresAt := metav1.NewTime(time.Now().Add(environmentDrainGracePeriod(env, environmentIdleTTL(env, time.Duration(h.cooldownMin)*time.Minute))))
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := runtimeAdapter.markDraining(ctx, env.Name, expiresAt); err != nil {
					slog.Error("failed to start draining", "err", err, "environment", env.Name)
					return
				}
				slog.Info("environment draining", "environment", env.Name, "expires_in", environmentDrainGracePeriod(env, environmentIdleTTL(env, time.Duration(h.cooldownMin)*time.Minute)).String())
			})
		},
	)
}

func (h *Handler) CloseTerminalWindow(c *gin.Context, challengeID, windowName string) {
	user := h.requireUser(c)
	if user == nil {
		return
	}
	windowName, err := parseTerminalWindow(windowName)
	if err != nil {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: err.Error()})
		return
	}
	entry, err := challenge.Get(h.challengesDir, challengeID)
	if err != nil {
		c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "challenge not found"})
		return
	}
	env, err := h.findEnvironment(c.Request.Context(), user.ID, entry)
	if err != nil || env.WorkspacePod == "" {
		c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "no active environment for this challenge"})
		return
	}
	sessionName := fmt.Sprintf("breakfix-%s", env.Name)
	if err := h.k8s.ClosePTYWindow(env.Namespace, env.WorkspacePod, sessionName, windowName); err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("close terminal window: %v", err)})
		return
	}
	closed := true
	c.JSON(http.StatusOK, api.TerminalWindowCloseResponse{Closed: &closed})
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

func (h *Handler) findProgressEnvironment(ctx context.Context, userID string, challengeEntry *challenge.Entry) (*activeEnvironment, error) {
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
	for i := range envs {
		env := envs[i]
		if env.Phase == breakfixv1.EnvironmentCompleted {
			return &env, nil
		}
	}
	return nil, errors.New("no environment with checkpoint status")
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

func challengeSummaryRuntime(runtime string) *api.ChallengeSummaryRuntime {
	value := api.ChallengeSummaryRuntime(challenge.NormalizeRuntime(runtime))
	return &value
}

func toAPICheckpoints(checkpoints []challenge.Checkpoint) []api.ChallengeCheckpoint {
	result := make([]api.ChallengeCheckpoint, 0, len(checkpoints))
	for _, checkpoint := range checkpoints {
		id := checkpoint.ID
		title := checkpoint.Title
		description := checkpoint.Description
		hint := checkpoint.Hint
		dependsOn := append([]string{}, checkpoint.DependsOn...)
		result = append(result, api.ChallengeCheckpoint{
			Id:          &id,
			Title:       &title,
			Description: &description,
			Hint:        &hint,
			DependsOn:   &dependsOn,
		})
	}
	return result
}

func toAPICheckResults(checks []challenge.CheckResult) []api.CheckpointResult {
	result := make([]api.CheckpointResult, 0, len(checks))
	for _, check := range checks {
		id := check.ID
		passed := check.Passed
		summary := check.Summary
		details := check.Details
		result = append(result, api.CheckpointResult{
			Id:      &id,
			Passed:  &passed,
			Summary: &summary,
			Details: &details,
		})
	}
	return result
}

func toAPICheckStatusResults(checks []breakfixv1.CheckpointResultStatus) []api.CheckpointResult {
	result := make([]api.CheckpointResult, 0, len(checks))
	for _, check := range checks {
		id := check.ID
		passed := check.Passed
		summary := check.Summary
		details := check.Details
		result = append(result, api.CheckpointResult{
			Id:      &id,
			Passed:  &passed,
			Summary: &summary,
			Details: &details,
		})
	}
	return result
}

func mustJSON(v any) string {
	data, _ := json.Marshal(v)
	return string(data)
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
