package server

import (
	"context"
	"fmt"
	"encoding/json"
	"sync"
	"time"

	"github.com/breakfix/breakfix/internal/auth"
	"github.com/breakfix/breakfix/internal/ca"
	"github.com/breakfix/breakfix/internal/config"
	"github.com/breakfix/breakfix/internal/db"
	"github.com/breakfix/breakfix/internal/k8s"
	pb "github.com/breakfix/breakfix/internal/proto"
	"github.com/breakfix/breakfix/internal/pty"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"
	"k8s.io/client-go/tools/remotecommand"
)

type Server struct {
	acrNS     string
	pb.UnimplementedBreakfixServer
	db            *db.DB
	k8s           *k8s.Client
	cooldown      *CooldownManager
	challengesDir string
	registry      string
	llm           config.LLMConfig
	ca            *ca.CA
}

func New(database *db.DB, client *k8s.Client, cooldown *CooldownManager, cfg config.Config, ca *ca.CA) *Server {
	return &Server{
		db:            database,
		k8s:           client,
		cooldown:      cooldown,
		challengesDir: cfg.ChallengesDir(),
		registry:      cfg.Registry,
		acrNS:         cfg.ACRNamespace,
		llm:           cfg.LLM,
		ca:            ca,
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

	// Check if user exists
	if _, err := s.db.GetUserBySubject(username); err == nil {
		return nil, status.Error(codes.AlreadyExists, "user already exists")
	}

	// Hash password
	passwordHash, err := auth.HashPassword(req.Password)
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to hash password")
	}

	// Generate TOTP secret
	secret, qr, err := auth.GenerateTOTPSecret(username)
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to generate TOTP")
	}

	// Create user
	id := fmt.Sprintf("u-%d", time.Now().UnixNano())
	_, err = s.db.CreateUserWithAuth(id, username, passwordHash, secret)
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to create user")
	}

	klog.InfoS("user registered", "user", username)

	return &pb.RegisterResponse{
		TotpSecret: secret,
		TotpQr:     qr,
		CaCert:     string(s.ca.CertPEM()),
	}, nil
}

func (s *Server) Login(ctx context.Context, req *pb.LoginRequest) (*pb.LoginResponse, error) {
	// Verify user
	user, err := s.db.GetUserBySubject(req.Username)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "invalid credentials")
	}

	// Verify password
	if !auth.CheckPassword(user.PasswordHash, req.Password) {
		return nil, status.Error(codes.Unauthenticated, "invalid credentials")
	}

	// Verify TOTP
	if !auth.ValidateTOTP(user.TOTPSecret, req.TotpCode) {
		return nil, status.Error(codes.Unauthenticated, "invalid TOTP code")
	}

	// Issue client certificate
	certPEM, keyPEM, err := s.ca.IssueClientCert(req.Username)
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to issue certificate")
	}

	klog.InfoS("user logged in", "user", req.Username)

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

	data, err := stream.Recv()
	if err != nil {
		return fmt.Errorf("receive instance id: %w", err)
	}
	instanceID := string(data.Data)

	inst, err := s.db.GetInstance(instanceID)
	if err != nil {
		return status.Error(codes.NotFound, "instance not found")
	}

	user, err := s.db.GetUserBySubject(subject)
	if err != nil {
		return status.Error(codes.PermissionDenied, "not your instance")
	}
	if inst.UserID != user.ID {
		return status.Error(codes.PermissionDenied, "not your instance")
	}

	klog.InfoS("pty session started", "instance", instanceID, "user", subject)

	resizeCh := make(chan remotecommand.TerminalSize, 4)
	rw := &pty.ReadWriter{Stream: stream, Resize: resizeCh}
	return s.k8s.ExecPTY(rw, rw, rw, resizeCh, inst.Namespace, inst.PodName)
}

// ── User ──

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


// ── Challenges ──

func (s *Server) ListChallenges(ctx context.Context, req *pb.ListChallengesRequest) (*pb.ListChallengesResponse, error) {
	challenges, err := s.db.ListChallenges()
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	subject, _ := s.getSubject(ctx)
	user, _ := s.db.GetUserBySubject(subject)

	var summaries []*pb.ChallengeSummary
	for _, c := range challenges {
		solved := false
		if user != nil {
			solved, _ = s.db.IsChallengeSolved(user.ID, c.ID)
		}
		summaries = append(summaries, &pb.ChallengeSummary{
			Id:         c.ID,
			Title:      c.Title,
			Type:       c.Type,
			Difficulty: c.Difficulty,
			Tags:       jsonParseTags(c.Tags),
			Solved:     solved,
		})
	}
	return &pb.ListChallengesResponse{Challenges: summaries}, nil
}

// ── Instances ──

func (s *Server) StartChallenge(ctx context.Context, req *pb.StartChallengeRequest) (*pb.StartChallengeResponse, error) {
	subject, err := s.getSubject(ctx)
	if err != nil {
		return nil, err
	}
	user, err := s.db.GetUserBySubject(subject)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "login required")
	}

	challenge, err := s.db.GetChallenge(req.ChallengeId)
	if err != nil {
		return nil, status.Error(codes.NotFound, "challenge not found")
	}

	instanceID := k8s.RandomID()
	ns := k8s.UserNamespace(s.acrNS, user.ID)
	podName := fmt.Sprintf("challenge-%s", instanceID)

	if err := s.k8s.EnsureNamespace(ns); err != nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("create namespace: %v", err))
	}
	if err := s.k8s.CreatePod(ns, podName, imageURL(challenge.Image, s.registry, s.acrNS), challenge.ID, instanceID); err != nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("create pod: %v", err))
	}
	if err := s.k8s.WaitForPod(ns, podName); err != nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("pod not ready: %v", err))
	}

	inst := db.Instance{
		ID:          instanceID,
		UserID:      user.ID,
		ChallengeID: challenge.ID,
		Status:      "running",
		Namespace:   ns,
		PodName:     podName,
	}
	if err := s.db.CreateInstance(inst); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	klog.InfoS("instance started", "instance", instanceID, "user", user.ID, "challenge", challenge.ID)

	return &pb.StartChallengeResponse{
		InstanceId:     instanceID,
		ChallengeTitle: challenge.Title,
		TimeoutSec:     int64(challenge.Timeout),
	}, nil
}

func (s *Server) GetInstance(ctx context.Context, req *pb.GetInstanceRequest) (*pb.GetInstanceResponse, error) {
	inst, err := s.db.GetInstance(req.InstanceId)
	if err != nil {
		return nil, status.Error(codes.NotFound, "instance not found")
	}
	var status_ pb.InstanceStatus
	switch inst.Status {
	case "running":
		status_ = pb.InstanceStatus_RUNNING
	case "draining":
		status_ = pb.InstanceStatus_DRAINING
	default:
		status_ = pb.InstanceStatus_DESTROYED
	}
	return &pb.GetInstanceResponse{
		InstanceId:   inst.ID,
		ChallengeId:  inst.ChallengeID,
		Status:       status_,
		RemainingSec: 0,
	}, nil
}

func (s *Server) PingInstance(ctx context.Context, req *pb.PingInstanceRequest) (*pb.PingInstanceResponse, error) {
	inst, err := s.db.GetInstance(req.InstanceId)
	if err != nil {
		return nil, status.Error(codes.NotFound, "instance not found")
	}
	s.cooldown.Cancel(req.InstanceId)
	if inst.Status == "draining" {
		if err := s.db.UpdateInstanceStatus(req.InstanceId, "running"); err != nil {
			klog.ErrorS(err, "failed to update instance status", "instance", req.InstanceId, "status", "running")
		}
		klog.V(2).InfoS("instance revived via ping", "instance", req.InstanceId)
	}
	return &pb.PingInstanceResponse{Status: pb.InstanceStatus_RUNNING}, nil
}

func (s *Server) StopChallenge(ctx context.Context, req *pb.StopChallengeRequest) (*pb.StopChallengeResponse, error) {
	inst, err := s.db.GetInstance(req.InstanceId)
	if err != nil {
		return nil, status.Error(codes.NotFound, "instance not found")
	}
	s.cooldown.Cancel(req.InstanceId)
	s.cleanupInstance(inst)
	return &pb.StopChallengeResponse{}, nil
}

func (s *Server) SubmitChallenge(ctx context.Context, req *pb.SubmitChallengeRequest) (*pb.SubmitChallengeResponse, error) {
	subject, err := s.getSubject(ctx)
	if err != nil {
		return nil, err
	}
	user, err := s.db.GetUserBySubject(subject)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "login required")
	}
	inst, err := s.db.GetInstance(req.InstanceId)
	if err != nil {
		return nil, status.Error(codes.NotFound, "instance not found")
	}
	if inst.UserID != user.ID {
		return nil, status.Error(codes.PermissionDenied, "not your instance")
	}
	challenge, err := s.db.GetChallenge(inst.ChallengeID)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	verifyPath := k8s.VerifyScriptPath(challenge.DirPath)
	if err := s.k8s.CopyToPod(inst.Namespace, inst.PodName, verifyPath, "/tmp/verify.sh"); err != nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("copy verify script: %v", err))
	}
	exitCode, output, err := s.k8s.ExecInPod(inst.Namespace, inst.PodName, "/bin/bash", "/tmp/verify.sh")
	if err != nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("verify failed: %v", err))
	}

	passed := exitCode == 0
	sub := db.Submission{
		InstanceID:  inst.ID,
		UserID:      user.ID,
		ChallengeID: inst.ChallengeID,
		Passed:      passed,
		ExitCode:    exitCode,
		Output:      func(s string) string { if len(s)>2000 { return s[:2000]+"..." }; return s }(output),
	}
	if err := s.db.CreateSubmission(sub); err != nil {
		klog.ErrorS(err, "failed to save submission", "instance", inst.ID)
	}

	s.cooldown.Cancel(req.InstanceId)
	s.cleanupInstance(inst)

	result := "FAILED"
	if passed {
		result = "PASSED"
	}
	klog.InfoS("submit result", "instance", req.InstanceId, "result", result, "exit", exitCode)

	return &pb.SubmitChallengeResponse{
		Passed:   passed,
		ExitCode: int32(exitCode),
		Output:   output,
	}, nil
}

// ── Cleanup ──

func (s *Server) cleanupInstance(inst *db.Instance) {
	if err := s.k8s.DeletePod(inst.Namespace, inst.PodName); err != nil {
		klog.ErrorS(err, "failed to delete pod", "namespace", inst.Namespace, "pod", inst.PodName)
	}
	if err := s.k8s.DeleteNamespace(inst.Namespace); err != nil {
		klog.ErrorS(err, "failed to delete namespace", "namespace", inst.Namespace)
	}
	if err := s.db.DestroyInstance(inst.ID); err != nil {
		klog.ErrorS(err, "failed to destroy instance record", "instance", inst.ID)
	}
}

// ── Tag parsing ──





// ── CooldownManager ──

type CooldownManager struct {
	db        *db.DB
	k8s       *k8s.Client
	mu        sync.Mutex
	timers    map[string]*time.Timer
	cleanupFn func(string) // called on cooldown expiry
}

func NewCooldownManager(database *db.DB, client *k8s.Client, cleanupFn func(string)) *CooldownManager {
	return &CooldownManager{
		db:        database,
		k8s:       client,
		timers:    make(map[string]*time.Timer),
		cleanupFn: cleanupFn,
	}
}

func (m *CooldownManager) SetCleanup(fn func(string)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanupFn = fn
}

func (m *CooldownManager) StartDraining(instanceID string) {

	m.mu.Lock()
	defer m.mu.Unlock()
	if t, ok := m.timers[instanceID]; ok {
		t.Stop()
	}
	if err := m.db.UpdateInstanceStatus(instanceID, "draining"); err != nil {
		klog.ErrorS(err, "failed to update instance status", "instance", instanceID, "status", "draining")
	}
	klog.V(1).InfoS("instance draining", "instance", instanceID, "cooldown", "5min")
	m.timers[instanceID] = time.AfterFunc(5*time.Minute, func() {
		m.destroy(instanceID)
	})
}

func (m *CooldownManager) Cancel(instanceID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if t, ok := m.timers[instanceID]; ok {
		t.Stop()
		delete(m.timers, instanceID)
	}
}



func (m *CooldownManager) destroy(instanceID string) {
	m.mu.Lock()
	delete(m.timers, instanceID)
	m.mu.Unlock()

	klog.InfoS("cooldown expired, destroying instance", "instance", instanceID)
	m.cleanupFn(instanceID)
}

func (m *CooldownManager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, t := range m.timers {
		t.Stop()
		delete(m.timers, id)
	}
}

func jsonParseTags(raw string) []string {
	var tags []string
	json.Unmarshal([]byte(raw), &tags)
	return tags
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}
func (s *Server) CleanupInstance(instanceID string) {
	inst, err := s.db.GetInstance(instanceID)
	if err != nil { return }
	s.cleanupInstance(inst)
}

func imageURL(image, registry, acrNS string) string {
	if registry == "" { return image }
	return registry + "/" + acrNS + "/" + image
}
