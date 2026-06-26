package server

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/breakfix/breakfix/internal/build"
	"github.com/breakfix/breakfix/internal/db"
	"github.com/breakfix/breakfix/internal/k8s"
	pb "github.com/breakfix/breakfix/internal/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Server struct {
	pb.UnimplementedBreakfixServer
	db            *db.DB
	k8s           *k8s.Client
	cooldown      *CooldownManager
	challengesDir string
}

func New(database *db.DB, client *k8s.Client, cooldown *CooldownManager, challengesDir string) *Server {
	return &Server{
		db:            database,
		k8s:           client,
		cooldown:      cooldown,
		challengesDir: challengesDir,
	}
}

// getSubject extracts user identity from context metadata (dev) or TLS cert (prod)
func (s *Server) getSubject(ctx context.Context) (string, error) {
	if build.IsDev() {
		return "dev-user", nil
	}
	// TODO: extract from mTLS cert in prod mode
	return "", status.Error(codes.Unimplemented, "mTLS auth not implemented")
}

func (s *Server) WhoAmI(ctx context.Context, req *pb.WhoAmIRequest) (*pb.WhoAmIResponse, error) {
	subject := req.Subject
	if subject == "" {
		var err error
		subject, err = s.getSubject(ctx)
		if err != nil {
			return nil, err
		}
	}

	user, isNew, err := s.db.GetOrCreateUser(subject, subject)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &pb.WhoAmIResponse{
		UserId: user.ID,
		Name:   user.Name,
		IsNew:  isNew,
	}, nil
}

func (s *Server) ListChallenges(ctx context.Context, req *pb.ListChallengesRequest) (*pb.ListChallengesResponse, error) {
	subject, err := s.getSubject(ctx)
	if err != nil {
		return nil, err
	}
	user, _, err := s.db.GetOrCreateUser(subject, subject)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	challenges, err := s.db.ListChallenges()
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	var summaries []*pb.ChallengeSummary
	for _, c := range challenges {
		solved, _ := s.db.IsChallengeSolved(user.ID, c.ID)
		summaries = append(summaries, &pb.ChallengeSummary{
			Id:         c.ID,
			Title:      c.Title,
			Type:       c.Type,
			Difficulty: c.Difficulty,
			Tags:       parseTags(c.Tags),
			Solved:     solved,
		})
	}

	return &pb.ListChallengesResponse{Challenges: summaries}, nil
}

func (s *Server) StartChallenge(ctx context.Context, req *pb.StartChallengeRequest) (*pb.StartChallengeResponse, error) {
	subject, err := s.getSubject(ctx)
	if err != nil {
		return nil, err
	}
	user, _, err := s.db.GetOrCreateUser(subject, subject)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	challenge, err := s.db.GetChallenge(req.ChallengeId)
	if err != nil {
		return nil, status.Error(codes.NotFound, "challenge not found")
	}

	instanceID := k8s.RandomID()
	ns := k8s.UserNamespace(user.ID)
	podName := fmt.Sprintf("challenge-%s", instanceID)

	if err := s.k8s.EnsureNamespace(ns); err != nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("create namespace: %v", err))
	}

	if err := s.k8s.CreatePod(ns, podName, challenge.Image, challenge.ID, instanceID); err != nil {
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

	log.Printf("Instance %s started for user %s, challenge %s", instanceID, user.ID, challenge.ID)

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

	remaining := s.cooldown.Remaining(req.InstanceId)

	return &pb.GetInstanceResponse{
		InstanceId:   inst.ID,
		ChallengeId:  inst.ChallengeID,
		Status:       status_,
		RemainingSec: int64(remaining.Seconds()),
	}, nil
}

func (s *Server) PingInstance(ctx context.Context, req *pb.PingInstanceRequest) (*pb.PingInstanceResponse, error) {
	inst, err := s.db.GetInstance(req.InstanceId)
	if err != nil {
		return nil, status.Error(codes.NotFound, "instance not found")
	}

	s.cooldown.Cancel(req.InstanceId)

	if inst.Status == "draining" {
		_ = s.db.UpdateInstanceStatus(req.InstanceId, "running")
		log.Printf("Instance %s revived via ping", req.InstanceId)
	}

	return &pb.PingInstanceResponse{
		Status: pb.InstanceStatus_RUNNING,
	}, nil
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
	user, _, err := s.db.GetOrCreateUser(subject, subject)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
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

	// Copy verify.sh to pod
	verifyPath := k8s.VerifyScriptPath(challenge.DirPath)
	if err := s.k8s.CopyToPod(inst.Namespace, inst.PodName, verifyPath, "/tmp/verify.sh"); err != nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("copy verify script: %v", err))
	}

	// Execute verify.sh
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
		Output:      truncate(output, 2000),
	}
	if err := s.db.CreateSubmission(sub); err != nil {
		log.Printf("Failed to save submission: %v", err)
	}

	s.cooldown.Cancel(req.InstanceId)
	s.cleanupInstance(inst)

	result := "FAILED"
	if passed {
		result = "PASSED"
	}
	log.Printf("Submit %s: %s (exit=%d)", req.InstanceId, result, exitCode)

	return &pb.SubmitChallengeResponse{
		Passed:   passed,
		ExitCode: int32(exitCode),
		Output:   output,
	}, nil
}

func (s *Server) cleanupInstance(inst *db.Instance) {
	_ = s.k8s.DeletePod(inst.Namespace, inst.PodName)
	_ = s.k8s.DeleteNamespace(inst.Namespace)
	_ = s.db.DestroyInstance(inst.ID)
}

func parseTags(raw string) []string {
	if raw == "" || raw == "[]" {
		return nil
	}
	var tags []string
	for _, t := range splitRaw(raw) {
		t = trim(t)
		if t != "" {
			tags = append(tags, t)
		}
	}
	return tags
}

func splitRaw(s string) []string {
	s = trim(s, '[', ']', '"', ' ')
	result := []string{}
	current := ""
	for _, c := range s {
		if c == ',' {
			result = append(result, current)
			current = ""
		} else {
			current += string(c)
		}
	}
	if current != "" {
		result = append(result, current)
	}
	return result
}

func trim(s string, cut ...rune) string {
	for _, r := range cut {
		for len(s) > 0 && rune(s[0]) == r {
			s = s[1:]
		}
		for len(s) > 0 && rune(s[len(s)-1]) == r {
			s = s[:len(s)-1]
		}
	}
	return s
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// CooldownManager handles the 5-minute draining timer
type CooldownManager struct {
	db     *db.DB
	k8s    *k8s.Client
	mu     sync.Mutex
	timers map[string]*time.Timer // instanceID -> timer
}

func NewCooldownManager(database *db.DB, client *k8s.Client) *CooldownManager {
	return &CooldownManager{
		db:     database,
		k8s:    client,
		timers: make(map[string]*time.Timer),
	}
}

func (m *CooldownManager) StartDraining(instanceID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if t, ok := m.timers[instanceID]; ok {
		t.Stop()
	}

	_ = m.db.UpdateInstanceStatus(instanceID, "draining")
	log.Printf("Instance %s draining (5min cooldown)", instanceID)

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

func (m *CooldownManager) Remaining(instanceID string) time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Only relevant for draining instances
	return 0
}

func (m *CooldownManager) CheckDraining() {
	insts, err := m.db.ListDrainingInstances()
	if err != nil {
		log.Printf("Cooldown check error: %v", err)
		return
	}
	for _, inst := range insts {
		m.mu.Lock()
		_, active := m.timers[inst.ID]
		m.mu.Unlock()
		if !active {
			m.StartDraining(inst.ID)
		}
	}
}

func (m *CooldownManager) destroy(instanceID string) {
	m.mu.Lock()
	delete(m.timers, instanceID)
	m.mu.Unlock()

	inst, err := m.db.GetInstance(instanceID)
	if err != nil {
		return
	}
	log.Printf("Cooldown expired, destroying instance %s", instanceID)
	_ = m.k8s.DeletePod(inst.Namespace, inst.PodName)
	_ = m.k8s.DeleteNamespace(inst.Namespace)
	_ = m.db.DestroyInstance(instanceID)
}

func (m *CooldownManager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, t := range m.timers {
		t.Stop()
		delete(m.timers, id)
	}
}
