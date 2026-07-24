package generator

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/breakfix/breakfix/internal/challenge"
	"github.com/cloudwego/eino/adk"
	"gopkg.in/yaml.v3"
)

// drainEvents processes agent events, logging all interactions.
func (g *Generator) drainEvents(events *adk.AsyncIterator[*adk.AgentEvent], onEvent func()) error {
	for {
		evt, ok := events.Next()
		if !ok {
			return nil
		}
		if onEvent != nil {
			onEvent()
		}
		if evt.Err != nil {
			slog.Error("agent error", "err", evt.Err)
			return evt.Err
		}
		if evt.Output == nil || evt.Output.MessageOutput == nil || evt.Output.MessageOutput.Message == nil {
			if evt.Action != nil && evt.Action.Exit {
				return nil
			}
			continue
		}
		msg := evt.Output.MessageOutput.Message
		for _, tc := range msg.ToolCalls {
			slog.Info("agent tool call", "tool", tc.Function.Name, "args", truncateStr(tc.Function.Arguments, 300))
		}
		if msg.ToolCallID != "" {
			slog.Info("agent tool result", "tool", msg.ToolName, "result", truncateStr(msg.Content, 300))
		} else if c := strings.TrimSpace(msg.Content); c != "" {
			slog.Info("agent text", "text", truncateStr(c, 300))
		}
		if r := strings.TrimSpace(msg.ReasoningContent); r != "" {
			slog.Info("agent reasoning", "content", truncateStr(r, 300))
		}
		if evt.Action != nil && evt.Action.Exit {
			return nil
		}
	}
}

func (g *Generator) watchGenerateQuiescence(ctx context.Context, chalDir string, lastEvent *atomic.Int64, quiesced *atomic.Bool, cancel context.CancelFunc) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	const idleThreshold = 15 * time.Second
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runtime, ready, modTime := generatedChallengeState(chalDir)
			if runtime != challenge.RuntimeVCluster || !ready {
				continue
			}
			last := time.Unix(0, lastEvent.Load())
			if time.Since(last) < idleThreshold || time.Since(modTime) < idleThreshold {
				continue
			}
			quiesced.Store(true)
			cancel()
			return
		}
	}
}

func generatedChallengeState(chalDir string) (runtime string, ready bool, modTime time.Time) {
	manifestPath := filepath.Join(chalDir, "challenge.yaml")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return "", false, time.Time{}
	}

	var spec map[string]any
	if err := yaml.Unmarshal(data, &spec); err != nil {
		return "", false, time.Time{}
	}
	runtime = challenge.NormalizeRuntime(scalarString(spec["runtime"]))

	required := []string{"challenge.yaml", "Dockerfile", "generate.sh", "problem.md", "solution.md", "checks/checkpoints.sh", "answer.sh"}
	for _, name := range required {
		info, err := os.Stat(filepath.Join(chalDir, name))
		if err != nil || info.IsDir() || info.Size() == 0 {
			return runtime, false, time.Time{}
		}
		if info.ModTime().After(modTime) {
			modTime = info.ModTime()
		}
	}
	return runtime, true, modTime
}

func truncateStr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
