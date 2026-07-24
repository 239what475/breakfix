package generator

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
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

	"github.com/breakfix/breakfix/internal/authoring"
	"github.com/breakfix/breakfix/internal/challenge"
	"github.com/breakfix/breakfix/internal/k8s"
	"gopkg.in/yaml.v3"
)

type Generator struct {
	Plan             *authoring.Plan
	OutputDir        string
	RegistryAddr     string
	RegistryInsecure bool
	Kubeconfig       string
	LabNS            string
	GenerationID     string
	ServerURL        string
	InternalAPIKey   string
	InitialFeedback  string
	SeedSubmissionID string
	AgentSessionID   string
	ResumeAgent      bool

	workDir      string
	artifactID   string
	agentStarted bool
}

const claudeRunAsNode = "/usr/local/bin/breakfix-claude"

const judgeTimeout = 10 * time.Minute

func (g *Generator) Run(ctx context.Context) error {
	g.agentStarted = g.ResumeAgent
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
	if strings.TrimSpace(g.SeedSubmissionID) != "" {
		seedArchive := filepath.Join(g.workDir, "verified-artifact.tar.gz")
		if err := downloadSubmission(ctx, VerifyTaskConfig{
			ServerURL:      g.ServerURL,
			InternalAPIKey: g.InternalAPIKey,
			SubmissionID:   g.SeedSubmissionID,
		}, seedArchive); err != nil {
			return fmt.Errorf("download verified artifact %s: %w", g.SeedSubmissionID, err)
		}
		seed, err := os.Open(seedArchive)
		if err != nil {
			return fmt.Errorf("open verified artifact %s: %w", g.SeedSubmissionID, err)
		}
		extractErr := challenge.ExtractTarGz(chalDir, seed)
		_ = seed.Close()
		if extractErr != nil {
			return fmt.Errorf("extract verified artifact %s: %w", g.SeedSubmissionID, extractErr)
		}
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

	judgeFeedback := strings.TrimSpace(g.InitialFeedback)

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
		if err := g.uploadArtifact(ctx, chalDir); err != nil {
			slog.Error("phase failed", "phase", "upload_artifact", "round", round+1, "duration", time.Since(uStart), "err", err)
			return err
		}
		slog.Info("phase done", "phase", "upload_artifact", "round", round+1, "duration", time.Since(uStart))
		slog.Info("round done", "round", round+1, "duration", time.Since(roundStart), "result", "artifact_uploaded")
		slog.Info("artifact generation done", "title", g.defaultTitle(), "artifactID", g.artifactID, "rounds", round+1, "duration", time.Since(genStart))
		return nil
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

	opts := []claudecode.Option{
		claudecode.WithBin(claudeRunAsNode),
		claudecode.WithSystemPrompt(WorkerSystemPrompt(g.RegistryAddr)),
		claudecode.WithTools(tools...),
		claudecode.WithCustomTools(customTools...),
		claudecode.WithMaxTurns(maxTurns),
		claudecode.WithCWD(chalDir),
		claudecode.WithAddDirs(g.OutputDir),
		claudecode.WithPermissionMode("acceptEdits"),
		claudecode.WithStderr(func(line string) { slog.Debug("claude", "msg", line) }),
	}
	if strings.TrimSpace(g.AgentSessionID) != "" {
		if g.agentStarted {
			opts = append(opts, claudecode.WithResume(g.AgentSessionID))
		} else {
			opts = append(opts, claudecode.WithSessionID(g.AgentSessionID))
		}
	}
	agent, err := claudecode.New(opts...)
	if err != nil {
		return fmt.Errorf("create worker agent: %w", err)
	}
	slog.Info("agent configured", "phase", "generate", "cwd", chalDir, "maxTurns", maxTurns, "toolCount", len(tools), "customToolCount", len(customTools), "timeout", timeout)

	var prompt string
	if judgeFeedback == "" {
		prompt = fmt.Sprintf(WorkerPromptCreate, g.reviewedPlanContext(), chalDir)
	} else {
		prompt = fmt.Sprintf(WorkerPromptFix, judgeFeedback, g.reviewedPlanContext(), chalDir)
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
	// A subsequent local judge retry or a later VerifyTask repair must resume
	// this same Claude Code conversation instead of regenerating without the
	// implementation context it has already established.
	if strings.TrimSpace(g.AgentSessionID) != "" {
		g.agentStarted = true
	}
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
	judgeCtx, cancel := context.WithTimeout(ctx, judgeTimeout)
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
	slog.Info("agent configured", "phase", "judge", "cwd", chalDir, "maxTurns", 8, "timeout", judgeTimeout)

	prompt := fmt.Sprintf(JudgePrompt, g.reviewedPlanContext(), fileContents.String())
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
			if msg.Content != "" {
				lastMsg = msg.Content
			}
		}
		if evt.Action != nil && evt.Action.Exit {
			if evt.Output != nil && evt.Output.MessageOutput != nil && evt.Output.MessageOutput.Message != nil {
				if content := evt.Output.MessageOutput.Message.Content; content != "" {
					lastMsg = content
				}
			}
			break
		}
	}
	if errors.Is(judgeCtx.Err(), context.DeadlineExceeded) {
		return false, "judge 超时，未能在限定时间内完成审核"
	}
	msg := lastMsg
	if msg == "" {
		return false, "judge 未返回 PASS/FAIL，视为审核失败；请直接检查 challenge.yaml、generate.sh、problem.md、solution.md、checks/checkpoints.sh、answer.sh 是否围绕同一套真实环境事实"
	}
	passed := judgeResponsePassed(msg)
	return passed, msg
}

// The judge protocol is one line: exactly PASS, or FAIL: followed by a reason.
// Any other model output is an invalid judgment and must not approve a challenge.
func judgeResponsePassed(message string) bool {
	return message == "PASS"
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

func (g *Generator) prefersVClusterAuthoring() bool {
	return g.Plan != nil && challenge.NormalizeRuntime(g.Plan.Metadata.Runtime) == challenge.RuntimeVCluster
}

func (g *Generator) defaultTitle() string {
	if g.Plan != nil && strings.TrimSpace(g.Plan.Metadata.Title) != "" {
		return g.Plan.Metadata.Title
	}
	return "Untitled challenge"
}

func (g *Generator) reviewedPlanContext() string {
	if g.Plan == nil {
		return "未提供已审阅的题目方案。"
	}
	metadata := g.Plan.Metadata
	parts := []string{
		fmt.Sprintf("标题：%s", metadata.Title),
		fmt.Sprintf("简介：%s", metadata.Description),
		fmt.Sprintf("难度：%s", metadata.Difficulty),
		fmt.Sprintf("标签：%s", strings.Join(metadata.Tags, "、")),
		fmt.Sprintf("运行时：%s", challenge.NormalizeRuntime(metadata.Runtime)),
		fmt.Sprintf("作者审核方案概览：\n%s", g.Plan.Overview),
	}
	for _, checkpoint := range g.Plan.SortedCheckpoints() {
		parts = append(parts, fmt.Sprintf("公开检查点 %d（%s）：\n%s", checkpoint.Position, checkpoint.Title, checkpoint.Markdown))
	}
	return strings.Join(parts, "\n\n")
}

func (g *Generator) uploadArtifact(ctx context.Context, chalDir string) error {
	if strings.TrimSpace(g.GenerationID) == "" {
		return fmt.Errorf("GENERATION_ID is required")
	}
	if strings.TrimSpace(g.ServerURL) == "" {
		return fmt.Errorf("SERVER_INTERNAL_URL is required")
	}
	if strings.TrimSpace(g.InternalAPIKey) == "" {
		return fmt.Errorf("SERVER_INTERNAL_API_KEY is required")
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

	url := strings.TrimRight(g.ServerURL, "/") + "/api/internal/generations/" + g.GenerationID + "/artifact"
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
