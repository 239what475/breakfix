package server

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/breakfix/breakfix/internal/auth"
	"github.com/breakfix/breakfix/internal/ca"
	"github.com/breakfix/breakfix/internal/config"
	"github.com/breakfix/breakfix/internal/db"
	"github.com/breakfix/breakfix/internal/k8s"
	pb "github.com/breakfix/breakfix/pkg/proto"
	breakfixv1 "github.com/breakfix/breakfix/apis/breakfix/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/remotecommand"
	"log/slog"
)

type Server struct {
	namespace        string
	crdNamespace     string
	cooldownMin      int
	pb.UnimplementedBreakfixServer
	db               *db.DB
	k8s              *k8s.Client
	challengesDir    string
	registryAddr     string
	registryInsecure bool
	llm              config.LLMConfig
	ca               *ca.CA
}

func New(database *db.DB, client *k8s.Client, cfg config.Config, ca *ca.CA) *Server {
	return &Server{
		db:               database,
		k8s:              client,
		challengesDir:    cfg.ChallengesDir(),
		registryAddr:     cfg.RegistryAddr,
		registryInsecure: cfg.RegistryInsecure,
		namespace:        cfg.Namespace,
		crdNamespace:     cfg.CRDNamespace,
		cooldownMin:      cfg.CooldownMinutes,
		llm:              cfg.LLM,
		ca:               ca,
	}
}

// ── Auth ──

func (s *Server) Register(ctx context.Context, req *pb.RegisterRequest) (*pb.RegisterResponse, error) {
	username := req.Username
	if username == "" || len(username) < 2 {
		return nil, status.Error(codes.InvalidArgument, "username too short")
	}
	if len(req.Password) < 6 {
		return nil, status.Error(codes.InvalidArgument, "password too short (min 6)")
	}
	if _, err := s.db.GetUserBySubject(username); err == nil {
		return nil, status.Error(codes.AlreadyExists, "user already exists")
	}

	passwordHash, err := auth.HashPassword(req.Password)
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to hash password")
	}
	secret, url, err := auth.GenerateTOTPSecret(username)
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to generate TOTP")
	}

	id := fmt.Sprintf("u-%d", time.Now().UnixNano())
	if _, err = s.db.CreateUserWithAuth(id, username, passwordHash, secret); err != nil {
		return nil, status.Error(codes.Internal, "failed to create user")
	}

	slog.Info("user registered", "user", username)
	return &pb.RegisterResponse{
		TotpSecret: secret,
		TotpUrl:    url,
		CaCert:     string(s.ca.CertPEM()),
	}, nil
}

func (s *Server) Login(ctx context.Context, req *pb.LoginRequest) (*pb.LoginResponse, error) {
	user, err := s.db.GetUserBySubject(req.Username)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "invalid credentials")
	}
	if !auth.CheckPassword(user.PasswordHash, req.Password) {
		return nil, status.Error(codes.Unauthenticated, "invalid credentials")
	}
	if !auth.ValidateTOTP(user.TOTPSecret, req.TotpCode) {
		return nil, status.Error(codes.Unauthenticated, "invalid TOTP code")
	}

	certPEM, keyPEM, err := s.ca.IssueClientCert(req.Username)
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to issue certificate")
	}

	slog.Info("user logged in", "user", req.Username)
	return &pb.LoginResponse{
		UserId:     user.ID,
		Name:       user.Name,
		ClientCert: string(certPEM),
		ClientKey:  string(keyPEM),
		CaCert:     string(s.ca.CertPEM()),
	}, nil
}

// ── Terminal ──

func (s *Server) ExecInstance(stream pb.Breakfix_ExecInstanceServer) error {
	subject, err := s.getSubject(stream.Context())
	if err != nil {
		return err
	}
	user, err := s.db.GetUserBySubject(subject)
	if err != nil {
		return status.Error(codes.Unauthenticated, "login required")
	}

	data, err := stream.Recv()
	if err != nil {
		return fmt.Errorf("receive challenge id: %w", err)
	}
	challengeID := string(data.Data)

	inst, err := s.findInstance(stream.Context(), user.ID, challengeID)
	if err != nil {
		return status.Error(codes.NotFound, "no active instance for this challenge")
	}
	if inst.Status.PodName == "" {
		return status.Error(codes.Unavailable, "pod not ready")
	}

	slog.Info("pty session started", "challenge", challengeID, "user", subject)

	resizeCh := make(chan remotecommand.TerminalSize, 4)
	rw := &ReadWriter{Stream: stream, Resize: resizeCh}
	execErr := s.k8s.ExecPTY(rw, rw, rw, resizeCh, inst.Status.Namespace, inst.Status.PodName)

	// On disconnect, start cooldown
	if inst.Status.Phase == breakfixv1.InstanceRunning {
		drainTime := metav1.NewTime(time.Now().Add(time.Duration(s.cooldownMin) * time.Minute))
		inst.Status.Phase = breakfixv1.InstanceDraining
		inst.Status.CooldownUntil = &drainTime
		if _, err := s.k8s.UpdateInstanceStatus(context.Background(), s.crdNamespace, inst); err != nil {
			slog.Error("failed to start draining", "err", err, "instance", inst.Name)
		}
		slog.Info("instance draining", "instance", inst.Name, "cooldown_min", s.cooldownMin)
	}

	return execErr
}

// ── Challenges ──

func (s *Server) ListChallenges(ctx context.Context, req *pb.ListChallengesRequest) (*pb.ListChallengesResponse, error) {
	challenges, err := s.db.ListChallenges()
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	subject, _ := s.getSubject(ctx)
	user, _ := s.db.GetUserBySubject(subject)

	solved := make(map[string]bool)
	active := make(map[string]bool)
	if user != nil {
		insts, err := s.k8s.ListInstances(ctx, s.crdNamespace, fmt.Sprintf("breakfix.dev/user=%s", user.ID))
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

	var summaries []*pb.ChallengeSummary
	for _, c := range challenges {
		summaries = append(summaries, &pb.ChallengeSummary{
			Id:         c.ID,
			Title:      c.Title,
			Type:       c.Type,
			Difficulty: c.Difficulty,
			Tags:       jsonParseTags(c.Tags),
			Solved:     solved[c.ID],
			Active:     active[c.ID],
		})
	}
	return &pb.ListChallengesResponse{Challenges: summaries}, nil
}

// ── Instances ──

func (s *Server) StartChallenge(ctx context.Context, req *pb.StartChallengeRequest) (*pb.StartChallengeResponse, error) {
	user, err := s.authenticate(ctx)
	if err != nil {
		return nil, err
	}
	challenge, err := s.db.GetChallenge(req.ChallengeId)
	if err != nil {
		return nil, status.Error(codes.NotFound, "challenge not found")
	}

	// Check for existing active instance
	existing, _ := s.findInstance(ctx, user.ID, req.ChallengeId)
	if existing != nil {
		slog.Info("resuming existing instance", "instance", existing.Name, "challenge", req.ChallengeId)
		return &pb.StartChallengeResponse{
			ChallengeTitle: challenge.Title,
		}, nil
	}

	// Create new instance
	inst, err := s.createInstance(ctx, user, challenge)
	if err != nil {
		return nil, err
	}

	slog.Info("instance started", "instance", inst.Name, "user", user.ID, "challenge", challenge.ID)
	return &pb.StartChallengeResponse{
		ChallengeTitle: challenge.Title,
	}, nil
}

func (s *Server) SubmitChallenge(ctx context.Context, req *pb.SubmitChallengeRequest) (*pb.SubmitChallengeResponse, error) {
	user, err := s.authenticate(ctx)
	if err != nil {
		return nil, err
	}
	inst, err := s.findInstance(ctx, user.ID, req.ChallengeId)
	if err != nil {
		return nil, status.Error(codes.NotFound, "no active instance for this challenge")
	}
	if inst.Status.Phase != breakfixv1.InstanceRunning && inst.Status.Phase != breakfixv1.InstanceDraining {
		return nil, status.Error(codes.FailedPrecondition, "instance not running")
	}

	// Trigger submission in controller
	inst.Spec.Submit = true
	if _, err := s.k8s.UpdateInstance(ctx, s.crdNamespace, inst); err != nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("update instance: %v", err))
	}

	result, err := s.waitSubmitResult(ctx, inst.Name, 30*time.Second)
	if err != nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("submit failed: %v", err))
	}

	return &pb.SubmitChallengeResponse{
		Passed:   result.Passed,
		ExitCode: int32(result.ExitCode),
		Output:   result.Output,
	}, nil
}

func (s *Server) ResetChallenge(ctx context.Context, req *pb.ResetChallengeRequest) (*pb.ResetChallengeResponse, error) {
	user, err := s.authenticate(ctx)
	if err != nil {
		return nil, err
	}
	challenge, err := s.db.GetChallenge(req.ChallengeId)
	if err != nil {
		return nil, status.Error(codes.NotFound, "challenge not found")
	}

	// Destroy existing instance if any
	existing, _ := s.findInstance(ctx, user.ID, req.ChallengeId)
	if existing != nil {
		existing.Status.Phase = breakfixv1.InstanceDestroyed
		if _, err := s.k8s.UpdateInstanceStatus(ctx, s.crdNamespace, existing); err != nil {
			slog.Error("failed to destroy old instance", "err", err)
		}
		// Wait for cleanup
		s.waitDestroyed(ctx, existing.Name)
		slog.Info("old instance destroyed", "instance", existing.Name)
	}

	// Create new instance
	inst, err := s.createInstance(ctx, user, challenge)
	if err != nil {
		return nil, err
	}

	slog.Info("instance reset", "instance", inst.Name, "user", user.ID, "challenge", challenge.ID)
	return &pb.ResetChallengeResponse{
		ChallengeTitle: challenge.Title,
	}, nil
}

// ── Agent ──

func (s *Server) GenerateChallenge(ctx context.Context, req *pb.GenerateChallengeRequest) (*pb.GenerateChallengeResponse, error) {
	topic := req.Topic
	if topic == "" {
		return nil, fmt.Errorf("topic required")
	}

	genID := "gen-" + k8s.RandomID()
	env := map[string]string{
		"TOPIC":                          topic,
		"REGISTRY_ADDR":                  s.registryAddr,
		"LAB_NAMESPACE":                  s.crdNamespace,
		"ANTHROPIC_BASE_URL":             s.llm.BaseURL,
		"ANTHROPIC_AUTH_TOKEN":           s.llm.APIKey,
		"ANTHROPIC_MODEL":                s.llm.Model,
		"ANTHROPIC_DEFAULT_OPUS_MODEL":   s.llm.Model,
		"ANTHROPIC_DEFAULT_SONNET_MODEL": s.llm.Model,
		"ANTHROPIC_DEFAULT_HAIKU_MODEL":  s.llm.HaikuModel,
		"CLAUDE_CODE_SUBAGENT_MODEL":     s.llm.HaikuModel,
		"CLAUDE_CODE_EFFORT_LEVEL":       s.llm.Effort,
	}
	if s.registryInsecure {
		env["REGISTRY_INSECURE"] = "true"
	}

	gen := &breakfixv1.Generation{
		ObjectMeta: metav1.ObjectMeta{
			Name:      genID,
			Namespace: s.crdNamespace,
		},
		Spec: breakfixv1.GenerationSpec{
			Topic: topic,
			Image: s.generatorImage(),
			Env:   env,
		},
	}

	if _, err := s.k8s.CreateGeneration(ctx, s.crdNamespace, gen); err != nil {
		return nil, fmt.Errorf("create generation: %w", err)
	}

	slog.Info("generation created", "generation", genID, "topic", topic)
	gen, err := s.waitGenerationDone(ctx, genID, 30*time.Minute)
	if err != nil {
		return &pb.GenerateChallengeResponse{Status: "failed", Detail: err.Error()}, nil
	}
	if gen.Status.Phase == breakfixv1.GenerationFailed {
		return &pb.GenerateChallengeResponse{Status: "failed", Detail: gen.Status.Message}, nil
	}
	if gen.Status.Challenge != nil {
		s.syncChallengeFromGeneration(gen)
	}

	return &pb.GenerateChallengeResponse{
		ChallengeId: gen.Status.Challenge.ID,
		Status:      "success",
		Detail:      fmt.Sprintf("challenge %s generated", gen.Status.Challenge.ID),
	}, nil
}

// ── Helpers ──

func (s *Server) getSubject(ctx context.Context) (string, error) {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return "", status.Error(codes.Unauthenticated, "no peer info")
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok {
		return "", status.Error(codes.Unauthenticated, "no TLS info")
	}
	if len(tlsInfo.State.PeerCertificates) == 0 {
		return "", status.Error(codes.Unauthenticated, "no client certificate")
	}
	return tlsInfo.State.PeerCertificates[0].Subject.CommonName, nil
}

func (s *Server) authenticate(ctx context.Context) (*db.User, error) {
	subject, err := s.getSubject(ctx)
	if err != nil {
		return nil, err
	}
	user, err := s.db.GetUserBySubject(subject)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "login required")
	}
	return user, nil
}

func (s *Server) generatorImage() string {
	if s.registryAddr == "" {
		return "breakfix-generator:latest"
	}
	return s.registryAddr + "/breakfix-generator:latest"
}

// findInstance returns the active (running or draining) instance for a user+challenge.
func (s *Server) findInstance(ctx context.Context, userID, challengeID string) (*breakfixv1.Instance, error) {
	selector := fmt.Sprintf("breakfix.dev/user=%s,breakfix.dev/challenge=%s", userID, challengeID)
	insts, err := s.k8s.ListInstances(ctx, s.crdNamespace, selector)
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

// createInstance creates a new Instance CRD and waits for the pod to be ready.
func (s *Server) createInstance(ctx context.Context, user *db.User, challenge *db.Challenge) (*breakfixv1.Instance, error) {
	instanceID := k8s.RandomID()
	inst := &breakfixv1.Instance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instanceID,
			Namespace: s.crdNamespace,
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

	if _, err := s.k8s.CreateInstance(ctx, s.crdNamespace, inst); err != nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("create instance: %v", err))
	}

	return s.waitInstanceReady(ctx, instanceID, 60*time.Second)
}

func (s *Server) waitInstanceReady(ctx context.Context, name string, timeout time.Duration) (*breakfixv1.Instance, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		inst, err := s.k8s.GetInstance(ctx, s.crdNamespace, name)
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

func (s *Server) waitSubmitResult(ctx context.Context, name string, timeout time.Duration) (*breakfixv1.SubmitResult, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		inst, err := s.k8s.GetInstance(ctx, s.crdNamespace, name)
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

func (s *Server) waitDestroyed(ctx context.Context, name string) {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		inst, err := s.k8s.GetInstance(ctx, s.crdNamespace, name)
		if err != nil {
			return // gone already
		}
		if inst.Status.Phase == breakfixv1.InstanceDestroyed && inst.Status.PodName == "" {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func (s *Server) waitGenerationDone(ctx context.Context, name string, timeout time.Duration) (*breakfixv1.Generation, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		gen, err := s.k8s.GetGeneration(ctx, s.crdNamespace, name)
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

func (s *Server) syncChallengeFromGeneration(gen *breakfixv1.Generation) {
	if gen.Status.Challenge == nil {
		return
	}
	cs := gen.Status.Challenge
	tags, _ := json.Marshal(cs.Tags)
	c := db.Challenge{
		ID:           cs.ID,
		Title:        cs.Title,
		Type:         cs.Type,
		Difficulty:   cs.Difficulty,
		Tags:         string(tags),
		Description:  cs.Description,
		Image:        cs.Image,
	}
	if err := s.db.UpsertChallenge(c); err != nil {
		slog.Error("failed to sync generated challenge", "err", err, "id", cs.ID)
	} else {
		slog.Info("challenge synced from generation", "id", cs.ID)
	}
}

func jsonParseTags(raw string) []string {
	var tags []string
	if err := json.Unmarshal([]byte(raw), &tags); err != nil {
		slog.Error("failed to parse tags", "err", err, "raw", raw)
	}
	return tags
}

// Ensure unused imports don't cause issues
