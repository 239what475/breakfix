package generator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	claudecode "github.com/239what475/eino-claude-code"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"log/slog"

	"github.com/breakfix/breakfix/internal/authoring"
	"github.com/breakfix/breakfix/internal/challenge"
	"github.com/breakfix/breakfix/internal/k8s"
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
		if err := downloadSubmission(ctx, g.ServerURL, g.InternalAPIKey, g.SeedSubmissionID, seedArchive); err != nil {
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
			slog.Info("phase failed", "phase", "validate_manifest", "round", round+1, "duration", time.Since(validateStart))
			slog.Info("round done", "round", round+1, "duration", time.Since(roundStart), "result", "artifact_invalid")
			continue
		}
		slog.Info("phase done", "phase", "validate_manifest", "round", round+1, "duration", time.Since(validateStart))

		semanticStart := time.Now()
		if err := g.validateChallengeSemantics(chalDir); err != nil {
			judgeFeedback = "challenge 语义检查失败: " + err.Error()
			slog.Info("phase failed", "phase", "validate_semantics", "round", round+1, "duration", time.Since(semanticStart))
			slog.Info("round done", "round", round+1, "duration", time.Since(roundStart), "result", "semantic_invalid")
			continue
		}
		slog.Info("phase done", "phase", "validate_semantics", "round", round+1, "duration", time.Since(semanticStart))

		jStart := time.Now()
		slog.Info("phase start", "phase", "judge", "round", round+1)
		passed, feedback := g.phaseJudge(ctx, chalDir)
		judgeFeedback = feedback
		slog.Info("phase done", "phase", "judge", "round", round+1, "duration", time.Since(jStart), "passed", passed)
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
	slog.Debug("agent prompt prepared", "phase", "generate", "chars", len(prompt))
	runner := adk.NewRunner(baseCtx, adk.RunnerConfig{Agent: agent})
	events := runner.Run(baseCtx, []adk.Message{schema.UserMessage(prompt)})

	err = g.drainEvents(events, nil)
	if strings.TrimSpace(g.AgentSessionID) != "" {
		g.agentStarted = true
	}
	if baseCtx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("generate phase timeout after %s", timeout)
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
	if lastMsg == "" {
		return false, "judge 未返回 PASS/FAIL，视为审核失败；请直接检查 challenge.yaml、generate.sh、problem.md、solution.md、checks/checkpoints.sh、answer.sh 是否围绕同一套真实环境事实"
	}
	return judgeResponsePassed(lastMsg), lastMsg
}

// The judge protocol is one line: exactly PASS, or FAIL: followed by a reason.
// Any other model output is an invalid judgment and must not approve a challenge.
func judgeResponsePassed(message string) bool {
	return message == "PASS"
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
		fmt.Sprintf("运行时：%s", challenge.NormalizeRuntime(metadata.Runtime)),
		fmt.Sprintf("作者审核方案概览：\n%s", g.Plan.Overview),
	}
	for _, checkpoint := range g.Plan.SortedCheckpoints() {
		parts = append(parts, fmt.Sprintf("公开检查点 %d（%s）：\n%s", checkpoint.Position, checkpoint.Title, checkpoint.Markdown))
	}
	return strings.Join(parts, "\n\n")
}

func runtimeHint(vcluster bool) string {
	if vcluster {
		return challenge.RuntimeVCluster
	}
	return challenge.RuntimeContainer
}
