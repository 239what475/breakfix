package generator

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	claudecode "github.com/239what475/eino-claude-code"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"log/slog"

	breakfixv1 "github.com/breakfix/breakfix/apis/breakfix/v1"
	"github.com/breakfix/breakfix/internal/api"
	"github.com/breakfix/breakfix/internal/challenge"
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

	workDir    string
	artifactID string
}

const claudeRunAsNode = "/usr/local/bin/breakfix-claude"

func (g *Generator) Run(ctx context.Context) error {
	if absOutput, err := filepath.Abs(g.OutputDir); err == nil {
		g.OutputDir = absOutput
	}
	g.artifactID = challenge.NewID()
	g.workDir = filepath.Join(g.OutputDir, ".gen-"+g.artifactID)
	if err := os.RemoveAll(g.workDir); err != nil {
		slog.Error("failed to clean workdir", "err", err, "dir", g.workDir)
	}
	chalDir := filepath.Join(g.workDir, g.artifactID)
	if err := os.MkdirAll(chalDir, 0755); err != nil {
		return fmt.Errorf("create challenge dir: %w", err)
	}
	if err := ensureClaudeWorkspaceWritable(g.workDir); err != nil {
		return fmt.Errorf("prepare claude workspace permissions: %w", err)
	}

	slog.Info("generator started", "title", g.defaultTitle(), "generationID", g.GenerationID, "artifactID", g.artifactID, "workDir", g.workDir)
	genStart := time.Now()

	k8sClient, err := k8s.New(g.Kubeconfig)
	if err != nil {
		return fmt.Errorf("k8s client: %w", err)
	}
	labClient := NewLabClient(k8sClient, g.LabNS, chalDir, g.RegistryAddr)
	labTools := labClient.Tools()

	var judgeFeedback string

	for round := 0; ; round++ {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("generation context ended: %w", err)
		}
		roundStart := time.Now()
		slog.Info("round start", "n", round+1, "generationID", g.GenerationID, "artifactID", g.artifactID)

		gStart := time.Now()
		slog.Info("phase start", "phase", "generate", "round", round+1, "runtimeHint", runtimeHint(g.prefersVClusterAuthoring()))
		if err := g.phaseGenerate(ctx, chalDir, labTools, judgeFeedback); err != nil {
			slog.Error("phase failed", "phase", "generate", "round", round+1, "duration", time.Since(gStart), "err", err)
			continue
		}
		slog.Info("phase done", "phase", "generate", "round", round+1, "duration", time.Since(gStart))

		validateStart := time.Now()
		if err := g.validateChallengeManifest(chalDir); err != nil {
			judgeFeedback = "challenge 文件结构或元数据不完整: " + err.Error()
			slog.Info("phase failed", "phase", "validate_manifest", "round", round+1, "duration", time.Since(validateStart), "feedback", truncateStr(judgeFeedback, 300))
			slog.Info("round done", "round", round+1, "duration", time.Since(roundStart), "result", "artifact_invalid", "feedback", truncateStr(judgeFeedback, 300))
			continue
		}
		slog.Info("phase done", "phase", "validate_manifest", "round", round+1, "duration", time.Since(validateStart))

		semanticStart := time.Now()
		if err := g.validateChallengeSemantics(chalDir); err != nil {
			judgeFeedback = "challenge 语义检查失败: " + err.Error()
			slog.Info("phase failed", "phase", "validate_semantics", "round", round+1, "duration", time.Since(semanticStart), "feedback", truncateStr(judgeFeedback, 300))
			slog.Info("round done", "round", round+1, "duration", time.Since(roundStart), "result", "semantic_invalid", "feedback", truncateStr(judgeFeedback, 300))
			continue
		}
		slog.Info("phase done", "phase", "validate_semantics", "round", round+1, "duration", time.Since(semanticStart))

		jStart := time.Now()
		slog.Info("phase start", "phase", "judge", "round", round+1)
		passed, feedback := g.phaseJudge(ctx, chalDir)
		judgeFeedback = feedback
		slog.Info("phase done", "phase", "judge", "round", round+1, "duration", time.Since(jStart), "passed", passed, "feedback", truncateStr(feedback, 200))
		if !passed {
			slog.Info("round done", "round", round+1, "duration", time.Since(roundStart), "result", "judge_fail")
			continue
		}

		uStart := time.Now()
		slog.Info("phase start", "phase", "upload_artifact", "round", round+1)
		verifyPassed, verifyFeedback, err := g.uploadArtifactAndWaitVerify(ctx, chalDir)
		if err != nil {
			slog.Error("phase failed", "phase", "upload_artifact", "round", round+1, "duration", time.Since(uStart), "err", err)
			judgeFeedback = err.Error()
			continue
		}
		slog.Info("phase done", "phase", "upload_artifact", "round", round+1, "duration", time.Since(uStart), "passed", verifyPassed)
		if verifyPassed {
			slog.Info("round done", "round", round+1, "duration", time.Since(roundStart), "result", "success")
			slog.Info("generation done", "title", g.defaultTitle(), "artifactID", g.artifactID, "rounds", round+1, "duration", time.Since(genStart))
			return nil
		}
		judgeFeedback = verifyFeedback
		slog.Info("round done", "round", round+1, "duration", time.Since(roundStart), "result", "verify_fail")
	}

}

func (g *Generator) phaseGenerate(ctx context.Context, chalDir string, labTools []tool.InvokableTool, judgeFeedback string) error {
	timeout := 12 * time.Minute
	if g.prefersVClusterAuthoring() {
		timeout = 8 * time.Minute
	}
	baseCtx, stop := context.WithTimeout(ctx, timeout)
	defer stop()

	runCtx, cancel := context.WithCancel(baseCtx)
	defer cancel()

	tools := []string{"Read", "Write", "Edit", "Bash"}
	customTools := labTools
	maxTurns := 40
	if g.prefersVClusterAuthoring() {
		tools = []string{"Read", "Write", "Edit"}
		customTools = nil
		maxTurns = 20
	}

	agent, err := claudecode.New(
		claudecode.WithBin(claudeRunAsNode),
		claudecode.WithSystemPrompt(WorkerSystemPrompt(g.RegistryAddr)),
		claudecode.WithTools(tools...),
		claudecode.WithCustomTools(customTools...),
		claudecode.WithMaxTurns(maxTurns),
		claudecode.WithNoSessionPersistence(true),
		claudecode.WithCWD(chalDir),
		claudecode.WithAddDirs(g.OutputDir),
		claudecode.WithPermissionMode("acceptEdits"),
		claudecode.WithStderr(func(line string) { slog.Debug("claude", "msg", line) }),
	)
	if err != nil {
		return fmt.Errorf("create worker agent: %w", err)
	}
	slog.Info("agent configured", "phase", "generate", "cwd", chalDir, "maxTurns", maxTurns, "toolCount", len(tools), "customToolCount", len(customTools), "timeout", timeout)

	var prompt string
	if judgeFeedback == "" {
		prompt = fmt.Sprintf(WorkerPromptCreate, g.draftContext(), chalDir)
	} else {
		prompt = fmt.Sprintf(WorkerPromptFix, judgeFeedback, g.draftContext(), chalDir)
	}
	slog.Info("agent prompt", "phase", "generate", "prompt", truncateStr(prompt, 500))
	runner := adk.NewRunner(runCtx, adk.RunnerConfig{Agent: agent})
	events := runner.Run(runCtx, []adk.Message{schema.UserMessage(prompt)})

	var lastEvent atomic.Int64
	lastEvent.Store(time.Now().UnixNano())
	var quiesced atomic.Bool
	go g.watchGenerateQuiescence(runCtx, chalDir, &lastEvent, &quiesced, cancel)

	err = g.drainEvents(events, func() {
		lastEvent.Store(time.Now().UnixNano())
	})
	if quiesced.Load() && (err == nil || errors.Is(err, context.Canceled)) {
		slog.Info("phase done", "phase", "generate", "mode", "vcluster_quiesced")
		return nil
	}
	if errors.Is(baseCtx.Err(), context.DeadlineExceeded) {
		err = fmt.Errorf("generate phase timeout after %s", timeout)
	}
	if err != nil && g.prefersVClusterAuthoring() {
		if runtime, ready, _ := generatedChallengeState(chalDir); runtime == challenge.RuntimeVCluster && ready {
			slog.Info("phase done", "phase", "generate", "mode", "vcluster_salvaged_after_agent_error", "err", err.Error())
			return nil
		}
	}
	return err
}

func (g *Generator) phaseJudge(ctx context.Context, chalDir string) (bool, string) {
	judgeCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()

	var fileContents strings.Builder
	_ = filepath.WalkDir(chalDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		rel, _ := filepath.Rel(chalDir, path)
		fmt.Fprintf(&fileContents, "\n--- %s ---\n%s\n", rel, string(data))
		return nil
	})

	agent, err := claudecode.New(
		claudecode.WithBin(claudeRunAsNode),
		claudecode.WithSystemPrompt(JudgeSystemPrompt()),
		claudecode.WithTools(),
		claudecode.WithMaxTurns(8),
		claudecode.WithNoSessionPersistence(true),
		claudecode.WithCWD(chalDir),
		claudecode.WithPermissionMode("acceptEdits"),
		claudecode.WithStderr(func(line string) { slog.Debug("claude", "msg", line) }),
	)
	if err != nil {
		slog.Error("create judge agent", "err", err)
		return false, ""
	}
	slog.Info("agent configured", "phase", "judge", "cwd", chalDir, "maxTurns", 8, "timeout", 3*time.Minute)

	prompt := fmt.Sprintf(JudgePrompt, g.draftContext(), fileContents.String())
	slog.Info("agent prompt", "phase", "judge", "prompt_len", len(prompt))
	runner := adk.NewRunner(judgeCtx, adk.RunnerConfig{Agent: agent})
	events := runner.Run(judgeCtx, []adk.Message{schema.UserMessage(prompt)})

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
	if errors.Is(judgeCtx.Err(), context.DeadlineExceeded) {
		return false, "judge 超时，未能在限定时间内完成审核"
	}
	msg := strings.TrimSpace(lastMsg)
	if msg == "" {
		return false, "judge 未返回 PASS/FAIL，视为审核失败；请直接检查 challenge.yaml、generate.sh、problem.md、solution.md、checks/checkpoints.sh、answer.sh 是否围绕同一套真实环境事实"
	}
	passed := judgeResponsePassed(msg)
	return passed, msg
}

// Judge agents occasionally wrap their required conclusion in a report. Accept
// only an explicit verdict, preferring the last one when a report has sections.
func judgeResponsePassed(message string) bool {
	var verdict string
	for _, raw := range strings.Split(message, "\n") {
		line := strings.Trim(strings.TrimSpace(raw), "*`")
		upper := strings.ToUpper(line)

		switch {
		case upper == "PASS" || strings.HasPrefix(upper, "PASS:"):
			verdict = "PASS"
		case upper == "FAIL" || strings.HasPrefix(upper, "FAIL:"):
			verdict = "FAIL"
		case strings.HasPrefix(upper, "FINAL VERDICT:") || strings.HasPrefix(upper, "OVERALL VERDICT:"):
			if strings.Contains(upper, "PASS") {
				verdict = "PASS"
			} else if strings.Contains(upper, "FAIL") {
				verdict = "FAIL"
			}
		}
	}
	if verdict != "" {
		return verdict == "PASS"
	}
	return strings.Contains(message, `"pass": true`)
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
	} else if scalarString(spec["type"]) != challenge.TypeScript {
		errs = append(errs, fmt.Sprintf("challenge.yaml type 必须为 %s，当前为 %q", challenge.TypeScript, scalarString(spec["type"])))
	}
	switch challenge.NormalizeRuntime(scalarString(spec["runtime"])) {
	case challenge.RuntimeContainer, challenge.RuntimeVCluster:
	default:
		errs = append(errs, fmt.Sprintf("challenge.yaml runtime 必须为 container/vcluster，当前为 %q", scalarString(spec["runtime"])))
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

func (g *Generator) validateChallengeSemantics(chalDir string) error {
	entry, err := loadChallengeEntry(chalDir)
	if err != nil {
		return err
	}
	dockerfileData, err := os.ReadFile(filepath.Join(chalDir, "Dockerfile"))
	if err != nil {
		return fmt.Errorf("read Dockerfile: %w", err)
	}

	checkpointPath := filepath.Join(chalDir, "checks", "checkpoints.sh")
	checkpointData, err := os.ReadFile(checkpointPath)
	if err != nil {
		return fmt.Errorf("read checks/checkpoints.sh: %w", err)
	}
	checkpointText := string(checkpointData)

	var errs []string
	if err := validateDockerfileNoBuildNetwork(string(dockerfileData)); err != nil {
		errs = append(errs, err.Error())
	}
	if challenge.NormalizeRuntime(entry.Runtime) == challenge.RuntimeVCluster {
		if err := validateKubectlPodReadinessPattern(checkpointText); err != nil {
			errs = append(errs, err.Error())
		}
		if err := validateVClusterCheckpointNoEphemeralProbePods(checkpointText); err != nil {
			errs = append(errs, err.Error())
		}
		if err := validateVClusterCheckpointNoNaivePodHealthLoop(checkpointText); err != nil {
			errs = append(errs, err.Error())
		}
		if err := validateVClusterCheckpointNoNaivePodGrepFilter(checkpointText); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

func validateDockerfileNoBuildNetwork(dockerfile string) error {
	normalized := strings.ToLower(dockerfile)
	for _, pattern := range []string{
		"apt-get", "apt install", "apk add", "yum install", "dnf install",
		"pip ", "npm install", "go install", "curl", "wget", "git clone", "add http",
	} {
		if strings.Contains(normalized, pattern) {
			return fmt.Errorf("Dockerfile 不得在构建期使用 %q 联网安装或下载内容；VerifyTask 构建没有外网。请只使用基础镜像已有工具，并把题目文件随 artifact 提供", pattern)
		}
	}
	return nil
}

func loadChallengeEntry(chalDir string) (*challenge.Entry, error) {
	return challenge.ValidateSubmissionDir(chalDir)
}

func validateKubectlPodReadinessPattern(verifyText string) error {
	normalized := strings.ReplaceAll(verifyText, " ", "")
	normalized = strings.ReplaceAll(normalized, "\t", "")

	badPatterns := []string{
		"Running\\s+1/1",
		"Running[[:space:]]+1/1",
		"Running.*1/1",
	}
	for _, pattern := range badPatterns {
		if strings.Contains(normalized, strings.ReplaceAll(pattern, " ", "")) {
			return fmt.Errorf("checks/checkpoints.sh 对 `kubectl get pods --no-headers` 的 READY/STATUS 列顺序判断错误：检测到 %q，这会把 `1/1   Running` 误判为失败；应按 `1/1` 在前、`Running` 在后设计匹配", pattern)
		}
	}
	return nil
}

func validateVClusterCheckpointNoEphemeralProbePods(checkpointText string) error {
	normalized := strings.ToLower(checkpointText)
	badSnippets := []string{
		"kubectl run",
		"busybox:1.36",
		"busybox:stable",
		"--rm -i --restart=never --image=",
	}
	for _, snippet := range badSnippets {
		if strings.Contains(normalized, snippet) {
			return fmt.Errorf("runtime=vcluster 的 checks/checkpoints.sh 不应依赖 `kubectl run` 拉外部探测镜像或临时 Pod；这会引入镜像可用性和时序不稳定，请改用现有工作负载、Service、endpoints 或 port-forward 等平台内可闭环的验证方式")
		}
	}
	return nil
}

func validateVClusterCheckpointNoNaivePodHealthLoop(checkpointText string) error {
	normalized := strings.ToLower(checkpointText)
	requiredSignals := []string{
		"kubectl get pods",
		"while ifs= read -r",
		"awk '{print $3}'",
		"awk '{print $2}'",
		"!= \"running\"",
		"!= \"1/1\"",
	}
	for _, signal := range requiredSignals {
		if !strings.Contains(normalized, signal) {
			return nil
		}
	}
	if strings.Contains(normalized, "deletiontimestamp") || strings.Contains(normalized, "ownerreferences") || strings.Contains(normalized, "rollout status") {
		return nil
	}
	return fmt.Errorf("runtime=vcluster 的 checks/checkpoints.sh 不应通过遍历标签下的所有 Pod 并硬判 `Running 1/1` 来验收；滚动更新期间旧 Pod 可能短暂处于 Terminating。请改为基于 workload 的 ready/available 条件，或只检查最终目标 Pod 集，并显式忽略 deletionTimestamp 不为空的旧 Pod")
}

func validateVClusterCheckpointNoNaivePodGrepFilter(checkpointText string) error {
	normalized := strings.ToLower(checkpointText)
	if !strings.Contains(normalized, "kubectl get pods") {
		return nil
	}
	if !strings.Contains(normalized, "grep -v") {
		return nil
	}
	if !strings.Contains(normalized, "1/1") || !strings.Contains(normalized, "running") {
		return nil
	}
	if strings.Contains(normalized, "deletiontimestamp") || strings.Contains(normalized, "ownerreferences") || strings.Contains(normalized, "rollout status") {
		return nil
	}
	return fmt.Errorf("runtime=vcluster 的 checks/checkpoints.sh 不应通过 `kubectl get pods ... | grep -v ... 1/1 ... Running` 这类全量 Pod 过滤方式直接判失败；滚动更新期间旧 Pod 可能短暂处于 Terminating。请改为基于 workload 的 ready/available 条件，或显式过滤 deletionTimestamp 不为空的旧 Pod")
}

func scalarString(v any) string {
	if v == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(v))
}

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

func (g *Generator) prefersVClusterAuthoring() bool {
	if g.Draft == nil {
		return false
	}
	text := strings.ToLower(strings.Join([]string{
		g.Draft.EnvironmentShape,
		g.Draft.Goal,
		g.Draft.Symptoms,
		g.Draft.FaultMechanism,
		g.Draft.AcceptanceCriteria,
		g.Draft.Description,
	}, "\n"))
	return strings.Contains(text, "kubectl") || strings.Contains(text, "kubernetes") || strings.Contains(text, "vcluster")
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
	slog.Info("artifact archived", "generationID", g.GenerationID, "artifactID", g.artifactID, "bytes", len(payload))

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("artifact", g.artifactID+".tar.gz")
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
	slog.Info("artifact uploaded", "generationID", g.GenerationID, "artifactID", g.artifactID, "status", resp.StatusCode)
	return nil
}

func archiveDir(root string) ([]byte, error) {
	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)

	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("archive does not allow symlink %s", path)
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("stat %s: %w", path, err)
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("archive does not allow non-regular file %s", path)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return fmt.Errorf("relative archive path for %s: %w", path, err)
		}
		name := filepath.ToSlash(rel)
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return fmt.Errorf("build tar header for %s: %w", path, err)
		}
		hdr.Name = name
		if info.IsDir() {
			hdr.Name += "/"
			hdr.Typeflag = tar.TypeDir
			hdr.Size = 0
		} else {
			hdr.Typeflag = tar.TypeReg
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return fmt.Errorf("write tar header for %s: %w", path, err)
		}
		if info.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		if _, err := tw.Write(data); err != nil {
			return fmt.Errorf("write tar body for %s: %w", path, err)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("archive challenge dir: %w", err)
	}

	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("close tar stream: %w", err)
	}
	if err := gzw.Close(); err != nil {
		return nil, fmt.Errorf("close gzip stream: %w", err)
	}
	return buf.Bytes(), nil
}

func (g *Generator) uploadArtifactAndWaitVerify(ctx context.Context, chalDir string) (bool, string, error) {
	slog.Info("artifact upload start", "generationID", g.GenerationID, "artifactID", g.artifactID)
	if err := g.uploadArtifact(ctx, chalDir); err != nil {
		return false, "", err
	}

	client := &http.Client{Timeout: 20 * time.Second}
	url := strings.TrimRight(g.GatewayURL, "/") + "/api/generate/jobs/" + g.GenerationID
	deadline := time.Now().Add(30 * time.Minute)
	lastStatus := ""
	lastMessage := ""
	lastLogAt := time.Time{}
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
		if status != lastStatus || message != lastMessage || lastLogAt.IsZero() || time.Since(lastLogAt) >= time.Minute {
			slog.Info("artifact verification poll", "generationID", g.GenerationID, "artifactID", g.artifactID, "status", status, "message", truncateStr(message, 200))
			lastStatus = status
			lastMessage = message
			lastLogAt = time.Now()
		}
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

func runtimeHint(vcluster bool) string {
	if vcluster {
		return challenge.RuntimeVCluster
	}
	return challenge.RuntimeContainer
}

func ensureClaudeWorkspaceWritable(path string) error {
	usr, err := user.Lookup("node")
	if err != nil {
		return nil
	}
	uid, err := strconv.Atoi(usr.Uid)
	if err != nil {
		return fmt.Errorf("parse node uid: %w", err)
	}
	gid, err := strconv.Atoi(usr.Gid)
	if err != nil {
		return fmt.Errorf("parse node gid: %w", err)
	}
	return filepath.Walk(path, func(current string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := os.Chown(current, uid, gid); err != nil {
			return err
		}
		return nil
	})
}
