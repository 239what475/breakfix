//go:build live

package taxonomy

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/config"
)

// TestLiveReviewPairUsesTypedTools calls the configured model twice through
// the actual Eino tool loop. It is intentionally opt-in: CI validates domain
// transitions deterministically, while this test verifies provider behavior
// and must run with an explicitly supplied API credential.
func TestLiveReviewPairUsesTypedTools(t *testing.T) {
	apiKey := strings.TrimSpace(os.Getenv("DEEPSEEK_API_KEY"))
	if apiKey == "" {
		t.Skip("DEEPSEEK_API_KEY is not configured")
	}
	baseURL := strings.TrimSpace(os.Getenv("DEEPSEEK_BASE_URL"))
	if baseURL == "" {
		baseURL = "https://api.deepseek.com"
	}
	model := strings.TrimSpace(os.Getenv("DEEPSEEK_MODEL"))
	if model == "" {
		model = "deepseek-v4-pro"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	curriculum, sre, err := runReviewPairWithEino(ctx, config.AgentConfig{
		BaseURL: baseURL, APIKeyEnv: "DEEPSEEK_API_KEY", APIKey: apiKey, Model: model, RequestTimeout: "90s",
	}, ExecutionContext{
		Stage:        WorkStageReview,
		WorkID:       "live-taxonomy-work",
		SystemPrompt: reviewerSystemPrompt,
		Prompt: `已验证 challenge：Repair cleanup logs (sha256:test)

当前 taxonomy：
{"revision":"","skills":[],"tags":[],"challengeMappings":[],"skillMappings":[]}

候选 ChangeSet：
{"skills":[],"tags":[],"challenge_mappings":[{"operation":"upsert","value":{"challenge":{"id":"challenge-test","title":"Repair cleanup logs","revision":"sha256:test"},"tags":[],"entry_skills":[],"outcomes":[]}}],"skill_mappings":[]}

challenge artifact：
--- problem.md ---
Repair a broken log cleanup task.
--- checks/checkpoints.sh ---
The checkpoint verifies that eligible logs are archived while protected logs remain.
`,
	})
	if err != nil {
		t.Fatalf("live taxonomy review pair failed: %v", err)
	}
	if err := ValidateReview(curriculum); err != nil {
		t.Fatalf("live curriculum result is invalid: %v", err)
	}
	if err := ValidateReview(sre); err != nil {
		t.Fatalf("live SRE result is invalid: %v", err)
	}
}
