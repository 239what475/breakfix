package generator

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	claudecode "github.com/239what475/eino-claude-code"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"gopkg.in/yaml.v3"
	"log/slog"

	breakfixv1 "github.com/breakfix/breakfix/apis/breakfix/v1"
	"github.com/breakfix/breakfix/internal/k8s"
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

		jStart := time.Now()
		passed, feedback := g.phaseJudge(ctx, chalDir)
		judgeFeedback = feedback
		slog.Info("phase done", "phase", "judge", "round", round+1, "duration", time.Since(jStart), "passed", passed, "feedback", truncateStr(feedback, 200))
		if !passed {
			slog.Info("round done", "round", round+1, "duration", time.Since(roundStart), "result", "judge_fail")
			continue
		}

		vStart := time.Now()
		verifyOK, verifyErr := g.phaseVerify(ctx, chalDir)
		if verifyOK {
			slog.Info("phase done", "phase", "verify", "round", round+1, "duration", time.Since(vStart))
			eStart := time.Now()
			if err := g.phaseEnrich(ctx, chalDir); err != nil {
				slog.Error("phase failed", "phase", "enrich", "round", round+1, "duration", time.Since(eStart), "err", err)
				continue
			}
			slog.Info("phase done", "phase", "enrich", "round", round+1, "duration", time.Since(eStart))
			uStart := time.Now()
			if err := g.uploadArtifact(ctx, chalDir); err != nil {
				slog.Error("phase failed", "phase", "upload", "round", round+1, "duration", time.Since(uStart), "err", err)
				continue
			}
			slog.Info("phase done", "phase", "upload", "round", round+1, "duration", time.Since(uStart))
			slog.Info("round done", "round", round+1, "duration", time.Since(roundStart), "result", "success")
			slog.Info("generation done", "title", g.defaultTitle(), "challengeID", g.challengeID, "rounds", round+1, "duration", time.Since(genStart))
			return g.finalize(chalDir)
		}
		judgeFeedback = "verify failed: " + verifyErr
		slog.Info("phase done", "phase", "verify", "round", round+1, "duration", time.Since(vStart), "passed", false, "err", verifyErr)
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

func (g *Generator) phaseVerify(ctx context.Context, chalDir string) (bool, string) {
	imageName := fmt.Sprintf("%s/%s:latest", g.RegistryAddr, g.challengeID)
	if !BuildAndPush(ctx, imageName, chalDir, g.RegistryInsecure) {
		return false, "image build or push failed"
	}

	k8sClient, err := k8s.New(g.Kubeconfig)
	if err != nil {
		slog.Error("k8s client", "err", err)
		return false, fmt.Sprintf("k8s client: %v", err)
	}
	ns := g.LabNS
	podName := "verify-" + g.challengeID

	k8sClient.EnsureNamespace(ns) //nolint:errcheck

	if err := k8sClient.CreatePod(ns, podName, k8s.CreatePodOpts{Image: imageName}); err != nil {
		slog.Error("create verify pod", "err", err)
		return false, fmt.Sprintf("create pod: %v", err)
	}
	defer k8sClient.DeletePod(ns, podName) //nolint:errcheck

	if err := k8sClient.WaitForPod(ns, podName, "challenge"); err != nil {
		slog.Error("wait verify pod", "err", err)
		return false, fmt.Sprintf("wait pod: %v", err)
	}

	answerPath := filepath.Join(chalDir, "answer.sh")
	if err := k8sClient.CopyToPod(ns, podName, answerPath, k8s.PodAnswerPath); err != nil {
		slog.Error("copy answer.sh", "err", err)
		return false, fmt.Sprintf("copy answer.sh: %v", err)
	}
	ansExit, ansOut, err := k8sClient.ExecInPod(ns, podName, "bash", k8s.PodAnswerPath)
	if err != nil {
		slog.Error("exec answer.sh", "err", err)
		return false, fmt.Sprintf("exec answer.sh: %v", err)
	}
	if ansExit != 0 {
		slog.Error("answer.sh failed", "exit", ansExit, "output", ansOut)
		return false, fmt.Sprintf("answer.sh exit=%d: %s", ansExit, ansOut)
	}

	verifyPath := filepath.Join(chalDir, "verify.sh")
	if err := k8sClient.CopyToPod(ns, podName, verifyPath, k8s.PodVerifyPath); err != nil {
		slog.Error("copy verify.sh", "err", err)
		return false, fmt.Sprintf("copy verify.sh: %v", err)
	}
	exitCode, output, err := k8sClient.ExecInPod(ns, podName, "bash", k8s.PodVerifyPath)
	if err != nil {
		slog.Error("exec verify.sh", "err", err)
		return false, fmt.Sprintf("exec verify.sh: %v", err)
	}

	if exitCode == 0 {
		slog.Info("verification PASSED")
		return true, ""
	}
	slog.Error("verification FAILED", "exit", exitCode, "output", output)
	return false, fmt.Sprintf("verify.sh exit=%d: %s", exitCode, output)
}

func (g *Generator) phaseEnrich(ctx context.Context, chalDir string) error {
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
		claudecode.WithSystemPrompt(EnrichSystemPrompt()),
		claudecode.WithTools("Read", "Write", "Edit"),
		claudecode.WithCWD(chalDir),
		claudecode.WithPermissionMode("acceptEdits"),
		claudecode.WithStderr(func(line string) { slog.Debug("claude", "msg", line) }),
	)
	if err != nil {
		return fmt.Errorf("create enrich agent: %w", err)
	}

	prompt := fmt.Sprintf(EnrichPrompt, g.draftContext(), fileContents.String())
	slog.Info("agent prompt", "phase", "enrich", "prompt_len", len(prompt))
	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent})
	events := runner.Run(ctx, []adk.Message{schema.UserMessage(prompt)})
	return g.drainEvents(events)
}

func (g *Generator) syncChallengeManifest(chalDir string) error {
	path := filepath.Join(chalDir, "challenge.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read challenge.yaml: %w", err)
	}

	var spec map[string]any
	if err := yaml.Unmarshal(data, &spec); err != nil {
		return fmt.Errorf("parse challenge.yaml: %w", err)
	}

	spec["id"] = g.challengeID
	spec["image"] = g.challengeID + ":latest"
	if strings.TrimSpace(fmt.Sprint(spec["type"])) == "" || fmt.Sprint(spec["type"]) == "<nil>" {
		spec["type"] = "script"
	}
	if strings.TrimSpace(fmt.Sprint(spec["title"])) == "" || fmt.Sprint(spec["title"]) == "<nil>" {
		spec["title"] = g.defaultTitle()
	}

	normalized, err := yaml.Marshal(spec)
	if err != nil {
		return fmt.Errorf("marshal challenge.yaml: %w", err)
	}
	if err := os.WriteFile(path, normalized, 0644); err != nil {
		return fmt.Errorf("write challenge.yaml: %w", err)
	}
	return nil
}

func (g *Generator) finalize(chalDir string) error {
	if err := g.syncChallengeManifest(chalDir); err != nil {
		return err
	}

	dst := filepath.Join(g.OutputDir, g.challengeID)
	if err := os.RemoveAll(dst); err != nil {
		slog.Error("failed to clean output", "err", err, "dir", dst)
	}
	if err := os.MkdirAll(dst, 0755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}

	entries, _ := os.ReadDir(chalDir)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(chalDir, e.Name()))
		if err != nil {
			slog.Error("failed to read generated file", "err", err, "name", e.Name())
			continue
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name()), data, 0644); err != nil { //nolint:gosec
			slog.Error("failed to write output file", "err", err, "name", e.Name())
		}
	}

	fmt.Println(g.challengeID)
	slog.Info("challenge finalized", "id", g.challengeID, "dir", dst)
	return nil
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

func (g *Generator) defaultDescription() string {
	if g.Draft != nil && strings.TrimSpace(g.Draft.Description) != "" {
		return g.Draft.Description
	}
	return g.defaultTitle()
}

func (g *Generator) draftContext() string {
	if g.Draft == nil {
		return "No reviewed challenge draft provided."
	}
	var parts []string
	parts = append(parts, fmt.Sprintf("Title: %s", g.Draft.Title))
	parts = append(parts, fmt.Sprintf("Difficulty: %s", g.Draft.Difficulty))
	if len(g.Draft.Tags) > 0 {
		parts = append(parts, fmt.Sprintf("Tags: %s", strings.Join(g.Draft.Tags, ", ")))
	}
	parts = append(parts, fmt.Sprintf("Description: %s", g.Draft.Description))
	parts = append(parts, fmt.Sprintf("Goal: %s", g.Draft.Goal))
	parts = append(parts, fmt.Sprintf("Symptoms: %s", g.Draft.Symptoms))
	parts = append(parts, fmt.Sprintf("Fault mechanism: %s", g.Draft.FaultMechanism))
	parts = append(parts, fmt.Sprintf("Environment shape: %s", g.Draft.EnvironmentShape))
	parts = append(parts, fmt.Sprintf("Acceptance criteria: %s", g.Draft.AcceptanceCriteria))
	parts = append(parts, fmt.Sprintf("Difficulty reason: %s", g.Draft.DifficultyReason))
	if strings.TrimSpace(g.Draft.Notes) != "" {
		parts = append(parts, fmt.Sprintf("Notes: %s", g.Draft.Notes))
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
	if err := g.syncChallengeManifest(chalDir); err != nil {
		return err
	}

	meta, err := g.challengeMetadata(chalDir)
	if err != nil {
		return err
	}
	payload, err := archiveDir(chalDir)
	if err != nil {
		return err
	}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fields := map[string]string{
		"challenge_id": meta.ID,
		"title":        meta.Title,
		"type":         meta.Type,
		"difficulty":   meta.Difficulty,
		"description":  meta.Description,
	}
	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			return fmt.Errorf("write multipart field %s: %w", k, err)
		}
	}
	tagsJSON, _ := json.Marshal(meta.Tags)
	if err := mw.WriteField("tags_b64", base64.StdEncoding.EncodeToString(tagsJSON)); err != nil {
		return fmt.Errorf("write multipart tags: %w", err)
	}
	part, err := mw.CreateFormFile("artifact", meta.ID+".tar.gz")
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

type challengeMetadata struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Type        string   `json:"type"`
	Difficulty  string   `json:"difficulty"`
	Tags        []string `json:"tags"`
	Description string   `json:"description"`
	Image       string   `json:"image"`
}

func (g *Generator) challengeMetadata(chalDir string) (*challengeMetadata, error) {
	metadata := &challengeMetadata{
		ID:          g.challengeID,
		Title:       g.defaultTitle(),
		Type:        "script",
		Difficulty:  "medium",
		Description: g.defaultDescription(),
		Image:       fmt.Sprintf("%s/%s:latest", g.RegistryAddr, g.challengeID),
	}
	if yamlData, err := os.ReadFile(filepath.Join(chalDir, "challenge.yaml")); err == nil {
		var spec struct {
			Title       string   `yaml:"title"`
			Type        string   `yaml:"type"`
			Difficulty  string   `yaml:"difficulty"`
			Tags        []string `yaml:"tags"`
			Description string   `yaml:"description"`
		}
		if err := yaml.Unmarshal(yamlData, &spec); err != nil {
			return nil, fmt.Errorf("parse challenge.yaml for metadata: %w", err)
		}
		if spec.Title != "" {
			metadata.Title = spec.Title
		}
		if spec.Type != "" {
			metadata.Type = spec.Type
		}
		if spec.Difficulty != "" {
			metadata.Difficulty = spec.Difficulty
		}
		if len(spec.Tags) > 0 {
			metadata.Tags = append([]string{}, spec.Tags...)
		}
		if spec.Description != "" {
			metadata.Description = spec.Description
		}
	}
	return metadata, nil
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
