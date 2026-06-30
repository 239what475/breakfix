package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/breakfix/breakfix/internal/api"
	"github.com/breakfix/breakfix/internal/auth"
	"github.com/breakfix/breakfix/internal/config"
	"github.com/breakfix/breakfix/internal/db"
	"github.com/breakfix/breakfix/internal/k8s"
	breakfixv1 "github.com/breakfix/breakfix/apis/breakfix/v1"
	"github.com/gin-gonic/gin"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"log/slog"
)

// Handler implements the OpenAPI-generated ServerInterface.
type Handler struct {
	db            *db.DB
	k8s           *k8s.Client
	registryAddr  string
	registryInsecure bool
	namespace     string
	crdNamespace  string
	cooldownMin   int
	llm           config.LLMConfig
	jwtSecret     []byte
}

func NewHandler(database *db.DB, client *k8s.Client, cfg config.Config) *Handler {
	return &Handler{
		db:               database,
		k8s:              client,
		registryAddr:     cfg.RegistryAddr,
		registryInsecure: cfg.RegistryInsecure,
		namespace:        cfg.Namespace,
		crdNamespace:     cfg.CRDNamespace,
		cooldownMin:      cfg.CooldownMinutes,
		llm:              cfg.LLM,
		jwtSecret:        []byte(cfg.JWTSecret),
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

	challenges, err := h.db.ListChallenges()
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: err.Error()})
		return
	}

	solved := make(map[string]bool)
	active := make(map[string]bool)
	if user != nil {
		insts, err := h.k8s.ListInstances(c.Request.Context(), h.crdNamespace, fmt.Sprintf("breakfix.dev/user=%s", user.ID))
		if err == nil {
			for _, inst := range insts.Items {
				if inst.Status.SubmitResult != nil && inst.Status.SubmitResult.Passed {
					solved[inst.Spec.ChallengeRef] = true
				}
				if inst.Status.Phase == breakfixv1.InstanceRunning || inst.Status.Phase == breakfixv1.InstanceDraining {
					active[inst.Spec.ChallengeRef] = true
				}
			}
		}
	}

	var summaries []api.ChallengeSummary
	for _, ch := range challenges {
		solvedVal := solved[ch.ID]
		activeVal := active[ch.ID]
		s := api.ChallengeSummary{
			Id:         &ch.ID,
			Title:      &ch.Title,
			Type:       &ch.Type,
			Difficulty: &ch.Difficulty,
			Description: &ch.Description,
			Solved:     &solvedVal,
			Active:     &activeVal,
		}
		var tags []string
		json.Unmarshal([]byte(ch.Tags), &tags)
		s.Tags = &tags
		summaries = append(summaries, s)
	}
	c.JSON(http.StatusOK, api.ChallengeList{Challenges: &summaries})
}

// ── Instances ──

func (h *Handler) StartChallenge(c *gin.Context, id string) {
	user := h.requireUser(c)
	if user == nil {
		return
	}

	challenge, err := h.db.GetChallenge(id)
	if err != nil {
		c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "challenge not found"})
		return
	}

	existing, _ := h.findInstance(c.Request.Context(), user.ID, id)
	if existing != nil {
		if existing.Status.Phase == breakfixv1.InstanceDraining {
			existing.Status.Phase = breakfixv1.InstanceRunning
			existing.Status.CooldownUntil = nil
			if _, err := h.k8s.UpdateInstanceStatus(c.Request.Context(), h.crdNamespace, existing); err != nil {
				c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("resume instance: %v", err)})
				return
			}
		}
		slog.Info("resuming existing instance", "instance", existing.Name, "challenge", id)
		title := challenge.Title
		c.JSON(http.StatusOK, api.StartResponse{ChallengeTitle: &title})
		return
	}

	inst, err := h.createInstance(c.Request.Context(), user, challenge)
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: err.Error()})
		return
	}

	slog.Info("instance started", "instance", inst.Name, "user", user.ID, "challenge", challenge.ID)
	title := challenge.Title
	c.JSON(http.StatusOK, api.StartResponse{ChallengeTitle: &title})
}

func (h *Handler) SubmitChallenge(c *gin.Context, id string) {
	user := h.requireUser(c)
	if user == nil {
		return
	}

	inst, err := h.findInstance(c.Request.Context(), user.ID, id)
	if err != nil {
		c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "no active instance for this challenge"})
		return
	}
	if inst.Status.Phase != breakfixv1.InstanceRunning && inst.Status.Phase != breakfixv1.InstanceDraining {
		c.JSON(http.StatusConflict, api.ErrorResponse{Error: "instance not running"})
		return
	}

	inst.Spec.Submit = true
	if _, err := h.k8s.UpdateInstance(c.Request.Context(), h.crdNamespace, inst); err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("update instance: %v", err)})
		return
	}

	result, err := h.waitSubmitResult(c.Request.Context(), inst.Name, 30*time.Second)
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("submit failed: %v", err)})
		return
	}

	exitCode := int(result.ExitCode)
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

	challenge, err := h.db.GetChallenge(id)
	if err != nil {
		c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "challenge not found"})
		return
	}

	existing, _ := h.findInstance(c.Request.Context(), user.ID, id)
	if existing != nil {
		existing.Status.Phase = breakfixv1.InstanceDestroyed
		if _, err := h.k8s.UpdateInstanceStatus(c.Request.Context(), h.crdNamespace, existing); err != nil {
			slog.Error("failed to destroy old instance", "err", err)
		}
		h.waitDestroyed(c.Request.Context(), existing.Name)
		slog.Info("old instance destroyed", "instance", existing.Name)
	}

	inst, err := h.createInstance(c.Request.Context(), user, challenge)
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: err.Error()})
		return
	}

	slog.Info("instance reset", "instance", inst.Name, "user", user.ID, "challenge", challenge.ID)
	title := challenge.Title
	c.JSON(http.StatusOK, api.ResetResponse{ChallengeTitle: &title})
}

// ── Agent ──

func (h *Handler) GenerateChallenge(c *gin.Context) {
	var req api.GenerateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: err.Error()})
		return
	}

	genID := "gen-" + k8s.RandomID()
	env := map[string]string{
		"TOPIC":                          req.Topic,
		"REGISTRY_ADDR":                  h.registryAddr,
		"LAB_NAMESPACE":                  h.crdNamespace,
		"ANTHROPIC_BASE_URL":             h.llm.BaseURL,
		"ANTHROPIC_AUTH_TOKEN":           h.llm.APIKey,
		"ANTHROPIC_MODEL":                h.llm.Model,
		"ANTHROPIC_DEFAULT_OPUS_MODEL":   h.llm.Model,
		"ANTHROPIC_DEFAULT_SONNET_MODEL": h.llm.Model,
		"ANTHROPIC_DEFAULT_HAIKU_MODEL":  h.llm.HaikuModel,
		"CLAUDE_CODE_SUBAGENT_MODEL":     h.llm.HaikuModel,
		"CLAUDE_CODE_EFFORT_LEVEL":       h.llm.Effort,
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
			Topic: req.Topic,
			Image: h.generatorImage(),
			Env:   env,
		},
	}

	if _, err := h.k8s.CreateGeneration(c.Request.Context(), h.crdNamespace, gen); err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("create generation: %v", err)})
		return
	}

	slog.Info("generation created", "generation", genID, "topic", req.Topic)
	gen, err := h.waitGenerationDone(c.Request.Context(), genID, 30*time.Minute)
	if err != nil {
		status := "failed"
		detail := err.Error()
		c.JSON(http.StatusOK, api.GenerateResponse{Status: &status, Detail: &detail})
		return
	}
	if gen.Status.Phase == breakfixv1.GenerationFailed {
		status := "failed"
		msg := gen.Status.Message
		c.JSON(http.StatusOK, api.GenerateResponse{Status: &status, Detail: &msg})
		return
	}
	if gen.Status.Challenge != nil {
		h.syncChallengeFromGeneration(gen)
	}

	status := "success"
	detail := fmt.Sprintf("challenge %s generated", gen.Status.Challenge.ID)
	cid := gen.Status.Challenge.ID
	c.JSON(http.StatusOK, api.GenerateResponse{
		ChallengeId: &cid,
		Status:      &status,
		Detail:      &detail,
	})
}

// ── Terminal (WebSocket) ──

func (h *Handler) HandleTerminal(c *gin.Context) {
	challengeID := c.Param("id")
	user := h.requireUser(c)
	if user == nil {
		return
	}

	inst, err := h.findInstance(c.Request.Context(), user.ID, challengeID)
	if err != nil {
		c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "no active instance for this challenge"})
		return
	}
	if inst.Status.PodName == "" {
		c.JSON(http.StatusServiceUnavailable, api.ErrorResponse{Error: "pod not ready"})
		return
	}

	slog.Info("terminal session started", "challenge", challengeID, "user", user.ID)
	wsUpgrade(c.Writer, c.Request, inst, h.k8s, h.crdNamespace, h.cooldownMin)
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

// ── Instance helpers ──

func (h *Handler) findInstance(ctx context.Context, userID, challengeID string) (*breakfixv1.Instance, error) {
	selector := fmt.Sprintf("breakfix.dev/user=%s,breakfix.dev/challenge=%s", userID, challengeID)
	insts, err := h.k8s.ListInstances(ctx, h.crdNamespace, selector)
	if err != nil {
		return nil, err
	}
	for i := range insts.Items {
		inst := &insts.Items[i]
		if inst.Status.Phase == breakfixv1.InstanceRunning || inst.Status.Phase == breakfixv1.InstanceDraining ||
			inst.Status.Phase == breakfixv1.InstancePending || inst.Status.Phase == "" {
			return inst, nil
		}
	}
	return nil, fmt.Errorf("no active instance for %s/%s", userID, challengeID)
}

func (h *Handler) createInstance(ctx context.Context, user *db.User, challenge *db.Challenge) (*breakfixv1.Instance, error) {
	instanceID := k8s.RandomID()
	inst := &breakfixv1.Instance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instanceID,
			Namespace: h.crdNamespace,
			Labels: map[string]string{
				"breakfix.dev/user":      user.ID,
				"breakfix.dev/challenge": challenge.ID,
			},
		},
		Spec: breakfixv1.InstanceSpec{
			ChallengeRef: challenge.ID,
			UserRef:      user.ID,
			Image:        challenge.Image,
		},
	}

	if _, err := h.k8s.CreateInstance(ctx, h.crdNamespace, inst); err != nil {
		return nil, fmt.Errorf("create instance: %w", err)
	}

	return h.waitInstanceReady(ctx, instanceID, 60*time.Second)
}

func (h *Handler) waitInstanceReady(ctx context.Context, name string, timeout time.Duration) (*breakfixv1.Instance, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		inst, err := h.k8s.GetInstance(ctx, h.crdNamespace, name)
		if err != nil {
			return nil, err
		}
		if inst.Status.Phase == breakfixv1.InstanceRunning {
			return inst, nil
		}
		if inst.Status.Phase == breakfixv1.InstanceDestroyed {
			return nil, fmt.Errorf("instance destroyed before ready")
		}
		time.Sleep(500 * time.Millisecond)
	}
	return nil, fmt.Errorf("timeout waiting for pod ready")
}

func (h *Handler) waitSubmitResult(ctx context.Context, name string, timeout time.Duration) (*breakfixv1.SubmitResult, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		inst, err := h.k8s.GetInstance(ctx, h.crdNamespace, name)
		if err != nil {
			return nil, err
		}
		if inst.Status.SubmitResult != nil {
			return inst.Status.SubmitResult, nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return nil, fmt.Errorf("timeout waiting for submit result")
}

func (h *Handler) waitDestroyed(ctx context.Context, name string) {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		inst, err := h.k8s.GetInstance(ctx, h.crdNamespace, name)
		if err != nil {
			return
		}
		if inst.Status.Phase == breakfixv1.InstanceDestroyed && inst.Status.PodName == "" {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func (h *Handler) waitGenerationDone(ctx context.Context, name string, timeout time.Duration) (*breakfixv1.Generation, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		gen, err := h.k8s.GetGeneration(ctx, h.crdNamespace, name)
		if err != nil {
			return nil, err
		}
		if gen.Status.Phase == breakfixv1.GenerationSucceeded || gen.Status.Phase == breakfixv1.GenerationFailed {
			return gen, nil
		}
		time.Sleep(2 * time.Second)
	}
	return nil, fmt.Errorf("timeout waiting for generation")
}

func (h *Handler) syncChallengeFromGeneration(gen *breakfixv1.Generation) {
	if gen.Status.Challenge == nil {
		return
	}
	cs := gen.Status.Challenge
	tags, _ := json.Marshal(cs.Tags)
	c := db.Challenge{
		ID:          cs.ID,
		Title:       cs.Title,
		Type:        cs.Type,
		Difficulty:  cs.Difficulty,
		Tags:        string(tags),
		Description: cs.Description,
		Image:       cs.Image,
	}
	if err := h.db.UpsertChallenge(c); err != nil {
		slog.Error("failed to sync generated challenge", "err", err, "id", cs.ID)
	} else {
		slog.Info("challenge synced from generation", "id", cs.ID)
	}
}
