package draftreview

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	claudecode "github.com/239what475/eino-claude-code"
	breakfixv1 "github.com/breakfix/breakfix/apis/breakfix/v1"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
	"log/slog"
)

type ReviewResult struct {
	Verdict  string                    `json:"verdict"`
	Reason   string                    `json:"reason"`
	Warnings []string                  `json:"warnings"`
	Draft    breakfixv1.ChallengeDraft `json:"draft"`
}

type Reviewer struct{}

func (r *Reviewer) Review(ctx context.Context, topic string) (*ReviewResult, error) {
	agent, err := claudecode.New(
		claudecode.WithSystemPrompt(systemPrompt()),
		claudecode.WithTools(),
		claudecode.WithPermissionMode("default"),
		claudecode.WithStderr(func(line string) { slog.Debug("draft-review", "msg", line) }),
	)
	if err != nil {
		return nil, fmt.Errorf("create review agent: %w", err)
	}

	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent})
	events := runner.Run(ctx, []adk.Message{schema.UserMessage(userPrompt(topic))})

	var lastMsg string
	for {
		evt, ok := events.Next()
		if !ok {
			break
		}
		if evt.Err != nil {
			return nil, evt.Err
		}
		if evt.Output != nil && evt.Output.MessageOutput != nil && evt.Output.MessageOutput.Message != nil {
			msg := evt.Output.MessageOutput.Message
			if c := strings.TrimSpace(msg.Content); c != "" {
				lastMsg = c
			}
		}
		if evt.Action != nil && evt.Action.Exit {
			break
		}
	}

	lastMsg = extractJSON(lastMsg)
	var result ReviewResult
	if err := json.Unmarshal([]byte(lastMsg), &result); err != nil {
		return nil, fmt.Errorf("parse review result: %w", err)
	}
	normalize(&result, topic)
	return &result, nil
}

func normalize(result *ReviewResult, topic string) {
	if strings.TrimSpace(result.Verdict) == "" {
		result.Verdict = "good"
	}
	if strings.TrimSpace(result.Reason) == "" {
		result.Reason = "The topic was expanded into a full challenge draft."
	}
	if strings.TrimSpace(result.Draft.Title) == "" {
		result.Draft.Title = topic
	}
	if strings.TrimSpace(result.Draft.Difficulty) == "" {
		result.Draft.Difficulty = "medium"
	}
	if len(result.Draft.Tags) == 0 {
		result.Draft.Tags = []string{"sre", "debugging"}
	}
	if strings.TrimSpace(result.Draft.Description) == "" {
		result.Draft.Description = topic
	}
}

func extractJSON(raw string) string {
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start >= 0 && end > start {
		return raw[start : end+1]
	}
	return raw
}

func systemPrompt() string {
	return `You are a senior SRE challenge curator for Breakfix.

Your job is NOT to build a challenge image or write scripts.
Your only job is to review a rough challenge idea and turn it into a high-quality structured challenge draft.

Return JSON only. No markdown fences. No explanation outside JSON.

Schema:
{
  "verdict": "good" | "weak" | "unsafe" | "duplicate-like",
  "reason": "short explanation",
  "warnings": ["warning 1", "warning 2"],
  "draft": {
    "title": "string",
    "difficulty": "easy|medium|hard",
    "tags": ["tag1", "tag2"],
    "description": "short Chinese user-facing summary",
    "operator_story": "what happened from the operator point of view",
    "broken_state": "what is broken in the environment",
    "expected_fix": "what kind of repair the user should make",
    "verification_expectations": "what verify.sh should prove",
    "constraints": "what makes the challenge realistic and non-trivial",
    "notes": "optional generation notes for the authoring pipeline"
  }
}

Requirements:
- Favor realistic Linux / SRE / infra terminal debugging tasks.
- Avoid trivia, pure config typos, or challenges with no investigation.
- Make the description concise and in Chinese.
- Keep the rest of the draft in clear English for the generator pipeline.
- If the idea is weak, still produce the best improved draft you can.`
}

func userPrompt(topic string) string {
	return fmt.Sprintf(`Review and expand this challenge idea into a complete structured draft:

%s`, topic)
}
