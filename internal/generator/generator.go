package generator

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	claudecode "github.com/239what475/eino-claude-code"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"log/slog"

	breakfixv1 "github.com/breakfix/breakfix/apis/breakfix/v1"
	"github.com/breakfix/breakfix/internal/api"
	"github.com/breakfix/breakfix/internal/k8s"
	"gopkg.in/yaml.v3"
)

type Generator struct {
	Draft            *breakfixv1.ChallengeDraft
	OutputDir        string
	RegistryAddr     string
	RegistryInsecure bool
	Kubeconfig       string
	LabNS            string
	GenerationID     string
	GatewayURL       string
	InternalAPIKey   string
	ServerJWT        string

	workDir     string
	challengeID string
}

func (g *Generator) Run(ctx context.Context) error {
	if absOutput, err := filepath.Abs(g.OutputDir); err == nil {
		g.OutputDir = absOutput
	}
	g.workDir = filepath.Join(g.OutputDir, ".gen-"+sanitizeID(g.identitySeed()))
	if err := os.RemoveAll(g.workDir); err != nil {
		slog.Error("failed to clean workdir", "err", err, "dir", g.workDir)
	}
	g.challengeID = sanitizeID(g.identitySeed())
	chalDir := filepath.Join(g.workDir, g.challengeID)
	if err := os.MkdirAll(chalDir, 0755); err != nil {
		return fmt.Errorf("create challenge dir: %w", err)
	}

	slog.Info("generator started", "title", g.defaultTitle(), "challengeID", g.challengeID)
	genStart := time.Now()

	k8sClient, err := k8s.New(g.Kubeconfig)
	if err != nil {
		return fmt.Errorf("k8s client: %w", err)
	}
	labClient := NewLabClient(k8sClient, g.LabNS, chalDir, g.RegistryAddr)
	labTools := labClient.Tools()

	var judgeFeedback string

	for round := 0; round < 5; round++ {
		roundStart := time.Now()
		slog.Info("round start", "n", round+1)

		gStart := time.Now()
		if err := g.phaseGenerate(ctx, chalDir, labTools, judgeFeedback); err != nil {
			slog.Error("phase failed", "phase", "generate", "round", round+1, "duration", time.Since(gStart), "err", err)
			continue
		}
		slog.Info("phase done", "phase", "generate", "round", round+1, "duration", time.Since(gStart))

		if err := g.validateChallengeManifest(chalDir); err != nil {
			judgeFeedback = "challenge 文件结构或元数据不完整: " + err.Error()
			slog.Info("round done", "round", round+1, "duration", time.Since(roundStart), "result", "artifact_invalid", "feedback", truncateStr(judgeFeedback, 300))
			continue
		}

		jStart := time.Now()
		passed, feedback := g.phaseJudge(ctx, chalDir)
		judgeFeedback = feedback
		slog.Info("phase done", "phase", "judge", "round", round+1, "duration", time.Since(jStart), "passed", passed, "feedback", truncateStr(feedback, 200))
		if !passed {
			slog.Info("round done", "round", round+1, "duration", time.Since(roundStart), "result", "judge_fail")
			continue
		}

		uStart := time.Now()
		verifyPassed, verifyFeedback, err := g.submitAndWaitVerify(ctx, chalDir)
		if err != nil {
			slog.Error("phase failed", "phase", "submit", "round", round+1, "duration", time.Since(uStart), "err", err)
			judgeFeedback = err.Error()
			continue
		}
		slog.Info("phase done", "phase", "submit", "round", round+1, "duration", time.Since(uStart), "passed", verifyPassed)
		if verifyPassed {
			slog.Info("round done", "round", round+1, "duration", time.Since(roundStart), "result", "success")
			slog.Info("generation done", "title", g.defaultTitle(), "challengeID", g.challengeID, "rounds", round+1, "duration", time.Since(genStart))
			return nil
		}
		judgeFeedback = verifyFeedback
		slog.Info("round done", "round", round+1, "duration", time.Since(roundStart), "result", "verify_fail")
	}

	return fmt.Errorf("exceeded max rounds for challenge draft: %s", g.defaultTitle())
}

func (g *Generator) phaseGenerate(ctx context.Context, chalDir string, labTools []tool.InvokableTool, judgeFeedback string) error {
	agent, err := claudecode.New(
		claudecode.WithSystemPrompt(WorkerSystemPrompt(g.RegistryAddr)),
		claudecode.WithTools("Read", "Write", "Edit", "Bash"),
		claudecode.WithCustomTools(labTools...),
		claudecode.WithCWD(chalDir),
		claudecode.WithAddDirs(g.OutputDir),
		claudecode.WithPermissionMode("acceptEdits"),
		claudecode.WithStderr(func(line string) { slog.Debug("claude", "msg", line) }),
	)
	if err != nil {
		return fmt.Errorf("create worker agent: %w", err)
	}

	var prompt string
	if judgeFeedback == "" {
		prompt = fmt.Sprintf(WorkerPromptCreate, g.draftContext(), chalDir)
	} else {
		prompt = fmt.Sprintf(WorkerPromptFix, judgeFeedback, g.draftContext(), chalDir)
	}
	slog.Info("agent prompt", "phase", "generate", "prompt", truncateStr(prompt, 500))
	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent})
	events := runner.Run(ctx, []adk.Message{schema.UserMessage(prompt)})
	return g.drainEvents(events)
}

func (g *Generator) phaseJudge(ctx context.Context, chalDir string) (bool, string) {
	var fileContents strings.Builder
	entries, _ := os.ReadDir(chalDir)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, _ := os.ReadFile(filepath.Join(chalDir, e.Name()))
		fmt.Fprintf(&fileContents, "\n--- %s ---\n%s\n", e.Name(), string(data))
	}

	agent, err := claudecode.New(
		claudecode.WithSystemPrompt(JudgeSystemPrompt()),
		claudecode.WithTools("Read"),
		claudecode.WithCWD(chalDir),
		claudecode.WithPermissionMode("acceptEdits"),
		claudecode.WithStderr(func(line string) { slog.Debug("claude", "msg", line) }),
	)
	if err != nil {
		slog.Error("create judge agent", "err", err)
		return false, ""
	}

	prompt := fmt.Sprintf(JudgePrompt, g.draftContext(), fileContents.String())
	slog.Info("agent prompt", "phase", "judge", "prompt_len", len(prompt))
	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent})
	events := runner.Run(ctx, []adk.Message{schema.UserMessage(prompt)})

	var lastMsg string
	for {
		evt, ok := events.Next()
		if !ok {
			break
		}
		if evt.Err != nil {
			return false, ""
		}
		if evt.Output != nil && evt.Output.MessageOutput != nil && evt.Output.MessageOutput.Message != nil {
			msg := evt.Output.MessageOutput.Message
			if c := strings.TrimSpace(msg.Content); c != "" {
				lastMsg = c
			}
		}
		if evt.Action != nil && evt.Action.Exit {
			if evt.Output != nil && evt.Output.MessageOutput != nil && evt.Output.MessageOutput.Message != nil {
				if c := strings.TrimSpace(evt.Output.MessageOutput.Message.Content); c != "" {
					lastMsg = c
				}
			}
			break
		}
	}
	passed := strings.Contains(lastMsg, "PASS") || strings.Contains(lastMsg, `"pass": true`)
	return passed, lastMsg
}

func (g *Generator) validateChallengeManifest(chalDir string) error {
	path := filepath.Join(chalDir, "challenge.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read challenge.yaml: %w", err)
	}

	var spec map[string]any
	if err := yaml.Unmarshal(data, &spec); err != nil {
		return fmt.Errorf("parse challenge.yaml: %w", err)
	}
	if spec == nil {
		spec = map[string]any{}
	}

	// id/image are platform-managed. Strip them if the agent wrote them.
	delete(spec, "id")
	delete(spec, "image")

	var errs []string
	if scalarString(spec["type"]) == "" {
		errs = append(errs, "challenge.yaml 缺少 type")
	} else if scalarString(spec["type"]) != "script" {
		errs = append(errs, fmt.Sprintf("challenge.yaml type 必须为 script，当前为 %q", scalarString(spec["type"])))
	}
	if scalarString(spec["title"]) == "" {
		errs = append(errs, "challenge.yaml 缺少 title")
	}
	switch scalarString(spec["difficulty"]) {
	case "":
		errs = append(errs, "challenge.yaml 缺少 difficulty")
	case "easy", "medium", "hard":
	default:
		errs = append(errs, fmt.Sprintf("challenge.yaml difficulty 必须为 easy/medium/hard，当前为 %q", scalarString(spec["difficulty"])))
	}
	var cleanTags []string
	if rawTags, ok := spec["tags"].([]any); ok {
		cleanTags = make([]string, 0, len(rawTags))
		for _, tag := range rawTags {
			tag := scalarString(tag)
			if tag != "" {
				cleanTags = append(cleanTags, tag)
			}
		}
	}
	if len(cleanTags) == 0 {
		errs = append(errs, "challenge.yaml 缺少非空 tags")
	}
	spec["tags"] = slices.Compact(cleanTags)
	if scalarString(spec["description"]) == "" {
		errs = append(errs, "challenge.yaml 缺少 description")
	}

	normalized, err := yaml.Marshal(spec)
	if err != nil {
		return fmt.Errorf("marshal challenge.yaml: %w", err)
	}
	if err := os.WriteFile(path, normalized, 0644); err != nil {
		return fmt.Errorf("write challenge.yaml: %w", err)
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

func scalarString(v any) string {
	if v == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(v))
}

// drainEvents processes agent events, logging all interactions.
func (g *Generator) drainEvents(events *adk.AsyncIterator[*adk.AgentEvent]) error {
	for {
		evt, ok := events.Next()
		if !ok {
			return nil
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

func truncateStr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

func sanitizeID(seed string) string {
	words := strings.Fields(strings.ToLower(seed))
	var id string
	for _, w := range words {
		clean := strings.Map(func(r rune) rune {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
				return r
			}
			return -1
		}, w)
		if len(clean) > 0 {
			if id != "" {
				id += "-"
			}
			id += clean
			if strings.Count(id, "-") >= 3 {
				break
			}
		}
	}
	if id == "" {
		h := fmt.Sprintf("%x", sum([]byte(seed)))
		id = "challenge-" + h[:8]
	}
	if len(id) > 50 {
		id = id[:50]
	}
	return strings.Trim(id, "-")
}

func (g *Generator) identitySeed() string {
	if g.Draft == nil {
		return "challenge"
	}
	if strings.TrimSpace(g.Draft.Title) != "" {
		return g.Draft.Title
	}
	if strings.TrimSpace(g.Draft.Description) != "" {
		return g.Draft.Description
	}
	return "challenge"
}

func (g *Generator) defaultTitle() string {
	if g.Draft != nil && strings.TrimSpace(g.Draft.Title) != "" {
		return g.Draft.Title
	}
	return "Untitled challenge"
}

func (g *Generator) draftContext() string {
	if g.Draft == nil {
		return "未提供已审阅的题目草案。"
	}
	var parts []string
	parts = append(parts, fmt.Sprintf("标题：%s", g.Draft.Title))
	parts = append(parts, fmt.Sprintf("难度：%s", g.Draft.Difficulty))
	if len(g.Draft.Tags) > 0 {
		parts = append(parts, fmt.Sprintf("标签：%s", strings.Join(g.Draft.Tags, "、")))
	}
	parts = append(parts, fmt.Sprintf("说明：%s", g.Draft.Description))
	parts = append(parts, fmt.Sprintf("目标：%s", g.Draft.Goal))
	parts = append(parts, fmt.Sprintf("表象：%s", g.Draft.Symptoms))
	parts = append(parts, fmt.Sprintf("故障机制：%s", g.Draft.FaultMechanism))
	parts = append(parts, fmt.Sprintf("环境形态：%s", g.Draft.EnvironmentShape))
	parts = append(parts, fmt.Sprintf("验收标准：%s", g.Draft.AcceptanceCriteria))
	parts = append(parts, fmt.Sprintf("难度理由：%s", g.Draft.DifficultyReason))
	if strings.TrimSpace(g.Draft.Notes) != "" {
		parts = append(parts, fmt.Sprintf("备注：%s", g.Draft.Notes))
	}
	return strings.Join(parts, "\n")
}

func sum(b []byte) [16]byte {
	var h [16]byte
	for i, c := range b {
		h[i%16] ^= c + byte(i)
	}
	return h
}

func (g *Generator) uploadArtifact(ctx context.Context, chalDir string) error {
	if strings.TrimSpace(g.GenerationID) == "" {
		return fmt.Errorf("GENERATION_ID is required")
	}
	if strings.TrimSpace(g.GatewayURL) == "" {
		return fmt.Errorf("GATEWAY_INTERNAL_URL is required")
	}
	if strings.TrimSpace(g.InternalAPIKey) == "" {
		return fmt.Errorf("GATEWAY_INTERNAL_API_KEY is required")
	}
	if err := g.validateChallengeManifest(chalDir); err != nil {
		return err
	}

	payload, err := archiveDir(chalDir)
	if err != nil {
		return err
	}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("artifact", g.challengeID+".tar.gz")
	if err != nil {
		return fmt.Errorf("create multipart artifact part: %w", err)
	}
	if _, err := part.Write(payload); err != nil {
		return fmt.Errorf("write artifact payload: %w", err)
	}
	if err := mw.Close(); err != nil {
		return fmt.Errorf("close multipart body: %w", err)
	}

	url := strings.TrimRight(g.GatewayURL, "/") + "/api/internal/generations/" + g.GenerationID + "/artifact"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &body)
	if err != nil {
		return fmt.Errorf("create upload request: %w", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-Breakfix-Internal-Key", g.InternalAPIKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("upload artifact: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("upload artifact: status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return nil
}

func archiveDir(root string) ([]byte, error) {
	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)

	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read challenge dir: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		path := filepath.Join(root, entry.Name())
		info, err := entry.Info()
		if err != nil {
			return nil, fmt.Errorf("stat %s: %w", path, err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		hdr := &tar.Header{
			Name:    entry.Name(),
			Mode:    int64(info.Mode().Perm()),
			Size:    int64(len(data)),
			ModTime: info.ModTime(),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, fmt.Errorf("write tar header for %s: %w", path, err)
		}
		if _, err := tw.Write(data); err != nil {
			return nil, fmt.Errorf("write tar body for %s: %w", path, err)
		}
	}

	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("close tar stream: %w", err)
	}
	if err := gzw.Close(); err != nil {
		return nil, fmt.Errorf("close gzip stream: %w", err)
	}
	return buf.Bytes(), nil
}

func (g *Generator) submitAndWaitVerify(ctx context.Context, chalDir string) (bool, string, error) {
	if err := g.uploadArtifact(ctx, chalDir); err != nil {
		return false, "", err
	}

	client := &http.Client{Timeout: 20 * time.Second}
	url := strings.TrimRight(g.GatewayURL, "/") + "/api/generate/jobs/" + g.GenerationID
	deadline := time.Now().Add(30 * time.Minute)
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return false, "", fmt.Errorf("create generation poll request: %w", err)
		}
		if strings.TrimSpace(g.ServerJWT) != "" {
			req.Header.Set("Authorization", "Bearer "+g.ServerJWT)
		}
		resp, err := client.Do(req)
		if err != nil {
			time.Sleep(5 * time.Second)
			continue
		}
		var body api.GenerationJobResponse
		decodeErr := json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			time.Sleep(5 * time.Second)
			continue
		}
		if decodeErr != nil {
			return false, "", fmt.Errorf("decode generation status: %w", decodeErr)
		}

		status := strings.ToLower(strings.TrimSpace(ptrString(body.Status)))
		message := ptrString(body.Message)
		switch status {
		case "success":
			return true, message, nil
		case "failed":
			return false, message, nil
		}

		time.Sleep(5 * time.Second)
	}
	return false, "", fmt.Errorf("timeout waiting for generation verification result")
}

func ptrString(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}
