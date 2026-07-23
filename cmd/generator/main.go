package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"

	"github.com/breakfix/breakfix/internal/authoring"
	"github.com/breakfix/breakfix/internal/generator"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug})))

	if os.Getenv("BREAKFIX_MODE") == "verify" {
		if err := generator.RunVerifyTask(context.Background(), generator.VerifyTaskConfig{
			Kubeconfig:       os.Getenv("KUBECONFIG"),
			LabNS:            envOr("LAB_NAMESPACE", "breakfix-system"),
			RegistryAddr:     envOr("REGISTRY_ADDR", "172.18.0.1:5000/break-fix"),
			RegistryInsecure: os.Getenv("REGISTRY_INSECURE") == "true",
			GatewayURL:       os.Getenv("GATEWAY_INTERNAL_URL"),
			InternalAPIKey:   os.Getenv("GATEWAY_INTERNAL_API_KEY"),
			VerifyTaskID:     os.Getenv("VERIFY_TASK_ID"),
			VerifyTaskNS:     envOr("VERIFY_TASK_NAMESPACE", "breakfix-system"),
			SubmissionID:     os.Getenv("VERIFY_SUBMISSION_ID"),
		}); err != nil {
			slog.Error("verify task failed", "err", err)
			os.Exit(1)
		}
		return
	}

	plan, err := parsePlan(os.Getenv("CHALLENGE_PLAN_JSON"))
	if err != nil {
		slog.Error("invalid authoring plan", "err", err)
		os.Exit(1)
	}
	if plan == nil {
		slog.Error("invalid challenge input", "err", "CHALLENGE_PLAN_JSON is required")
		os.Exit(1)
	}

	g := &generator.Generator{
		Plan:             plan,
		OutputDir:        envOr("CHALLENGE_OUTPUT_DIR", "data/challenges"),
		RegistryAddr:     envOr("REGISTRY_ADDR", "172.18.0.1:5000/break-fix"),
		RegistryInsecure: os.Getenv("REGISTRY_INSECURE") == "true",
		Kubeconfig:       os.Getenv("KUBECONFIG"),
		LabNS:            envOr("LAB_NAMESPACE", "breakfix-system"),
		GenerationID:     os.Getenv("GENERATION_ID"),
		GatewayURL:       os.Getenv("GATEWAY_INTERNAL_URL"),
		InternalAPIKey:   os.Getenv("GATEWAY_INTERNAL_API_KEY"),
		InitialFeedback:  os.Getenv("GENERATION_FEEDBACK"),
		SeedSubmissionID: os.Getenv("GENERATION_BASE_SUBMISSION_ID"),
		AgentSessionID:   os.Getenv("GENERATION_AGENT_SESSION_ID"),
		ResumeAgent:      os.Getenv("GENERATION_AGENT_RESUME") == "true",
	}

	if err := g.Run(context.Background()); err != nil {
		slog.Error("generator failed", "err", err)
		os.Exit(1)
	}
}

func parsePlan(raw string) (*authoring.Plan, error) {
	if raw == "" {
		return nil, nil
	}
	var plan authoring.Plan
	if err := json.Unmarshal([]byte(raw), &plan); err != nil {
		return nil, err
	}
	if err := plan.ValidateForGeneration(); err != nil {
		return nil, err
	}
	return &plan, nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
