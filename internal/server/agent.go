package server

import (
	"context"
	"fmt"
	"strings"
	"time"

	pb "github.com/breakfix/breakfix/internal/proto"
	"log/slog"

	"github.com/breakfix/breakfix/internal/k8s"
)

const (
	generatorImage = "breakfix-generator:latest"
	generatorNS    = "breakfix-gen"
	jobTimeout     = 15 * time.Minute
)

// GenerateChallenge creates a K8s Job to run the agent workflow.
func (s *Server) GenerateChallenge(ctx context.Context, req *pb.GenerateChallengeRequest) (*pb.GenerateChallengeResponse, error) {
	topic := strings.TrimSpace(req.Topic)
	if topic == "" {
		return nil, fmt.Errorf("topic required")
	}

	// Ensure namespace exists
	if err := s.k8s.EnsureNamespace(generatorNS); err != nil {
		return nil, fmt.Errorf("ensure namespace: %w", err)
	}

	jobName := "gen-" + k8s.RandomID()

	env := map[string]string{
		"TOPIC":                          topic,
		"REGISTRY":                       s.registry,
		"ACR_NAMESPACE":                  s.namespace,
		"ANTHROPIC_BASE_URL":             s.llm.BaseURL,
		"ANTHROPIC_AUTH_TOKEN":           s.llm.APIKey,
		"ANTHROPIC_MODEL":                s.llm.Model,
		"ANTHROPIC_DEFAULT_OPUS_MODEL":   s.llm.Model,
		"ANTHROPIC_DEFAULT_SONNET_MODEL": s.llm.Model,
		"ANTHROPIC_DEFAULT_HAIKU_MODEL":  s.llm.HaikuModel,
		"CLAUDE_CODE_SUBAGENT_MODEL":     s.llm.HaikuModel,
		"CLAUDE_CODE_EFFORT_LEVEL":       s.llm.Effort,
	}

	slog.Info("creating generator job", "job", jobName, "topic", topic)

	if err := s.k8s.CreateJob(generatorNS, jobName, generatorImage, env); err != nil {
		return nil, fmt.Errorf("create job: %w", err)
	}

	ok, podName, err := s.k8s.WaitForJob(generatorNS, jobName, jobTimeout)

	// Read logs before any cleanup
	logs := s.readPodLogs(generatorNS, podName)

	// Cleanup asynchronously
	if podName != "" {
		go func() {
			if err := s.k8s.DeletePod(generatorNS, podName); err != nil {
				slog.Error("failed to cleanup generator pod", "err", err)
			}
		}()
	}

	if err != nil {
		slog.Error("job failed", "err", err, "job", jobName, "logs", logs)
		return &pb.GenerateChallengeResponse{Status: "failed", Detail: err.Error() + "\n" + logs}, nil
	}
	if !ok {
		return &pb.GenerateChallengeResponse{Status: "failed", Detail: "job did not succeed\n" + logs}, nil
	}

	slog.Info("job completed", "job", jobName, "pod", podName)
	return &pb.GenerateChallengeResponse{
		Status: "success",
		Detail: fmt.Sprintf("job %s completed\n%s", jobName, logs),
	}, nil
}

func (s *Server) readPodLogs(ns, podName string) string {
	if podName == "" {
		return ""
	}
	logs, err := s.k8s.ReadPodLogs(ns, podName)
	if err != nil {
		return fmt.Sprintf("(logs unavailable: %v)", err)
	}
	return logs
}
