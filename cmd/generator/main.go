package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"

	breakfixv1 "github.com/breakfix/breakfix/apis/breakfix/v1"
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

	draftJSON := flag.String("draft-json", "", "Reviewed challenge draft JSON")
	outputDir := flag.String("output", "data/challenges", "Output directory")
	flag.Parse()

	rawDraft := *draftJSON
	if rawDraft == "" {
		rawDraft = os.Getenv("CHALLENGE_DRAFT_JSON")
	}
	draft, err := parseDraft(rawDraft)
	if err != nil {
		slog.Error("invalid challenge draft", "err", err)
		os.Exit(1)
	}

	g := &generator.Generator{
		Draft:            draft,
		OutputDir:        envOr("CHALLENGE_OUTPUT_DIR", *outputDir),
		RegistryAddr:     envOr("REGISTRY_ADDR", "172.18.0.1:5000/break-fix"),
		RegistryInsecure: os.Getenv("REGISTRY_INSECURE") == "true",
		Kubeconfig:       os.Getenv("KUBECONFIG"),
		LabNS:            envOr("LAB_NAMESPACE", "breakfix-system"),
		GenerationID:     os.Getenv("GENERATION_ID"),
		GatewayURL:       os.Getenv("GATEWAY_INTERNAL_URL"),
		InternalAPIKey:   os.Getenv("GATEWAY_INTERNAL_API_KEY"),
		ServerJWT:        os.Getenv("BREAKFIX_GENERATION_JWT"),
	}

	if err := g.Run(context.Background()); err != nil {
		slog.Error("generator failed", "err", err)
		os.Exit(1)
	}
}

func parseDraft(raw string) (*breakfixv1.ChallengeDraft, error) {
	if raw == "" {
		return nil, fmt.Errorf("CHALLENGE_DRAFT_JSON or --draft-json is required")
	}

	var draft breakfixv1.ChallengeDraft
	if err := json.Unmarshal([]byte(raw), &draft); err != nil {
		return nil, err
	}
	if draft.Title == "" {
		return nil, fmt.Errorf("draft.title is required")
	}
	if draft.Description == "" {
		return nil, fmt.Errorf("draft.description is required")
	}
	return &draft, nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
