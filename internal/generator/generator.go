package generator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	claudecode "github.com/239what475/eino-claude-code"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"log/slog"

	"github.com/breakfix/breakfix/internal/k8s"
)

// Generator orchestrates the challenge generation workflow.
type Generator struct {
	Topic      string
	OutputDir  string
	Registry   string
	ACRNS      string
	Kubeconfig string

	workDir     string
	challengeID string
}

// Run executes the 3-phase generation loop.
func (g *Generator) Run(ctx context.Context) error {
	g.workDir = filepath.Join(g.OutputDir, ".gen-"+sanitizeID(g.Topic))
	if err := os.RemoveAll(g.workDir); err != nil {
		slog.Error("failed to clean workdir", "err", err, "dir", g.workDir)
	}
	g.challengeID = sanitizeID(g.Topic)
	chalDir := filepath.Join(g.workDir, g.challengeID)
	if err := os.MkdirAll(chalDir, 0755); err != nil {
		return fmt.Errorf("create challenge dir: %w", err)
	}

	slog.Info("generator started", "topic", g.Topic, "challengeID", g.challengeID)

	k8sClient, err := k8s.New(g.Kubeconfig)
	if err != nil {
		return fmt.Errorf("k8s client: %w", err)
	}
	labClient := NewLabClient(k8sClient, "breakfix-gen")
	labTools := labClient.Tools()

	for round := 0; round < 5; round++ {
		slog.Info("round", "n", round+1)

		if err := g.phaseGenerate(ctx, chalDir, labTools); err != nil {
			slog.Error("phase1 failed", "err", err, "round", round+1)
			continue
		}

		if !g.phaseJudge(ctx, chalDir) {
			slog.Info("phase2: FAIL, retrying", "round", round+1)
			continue
		}
		slog.Info("phase2: PASS")

		if g.phaseVerify(ctx, chalDir) {
			return g.finalize(chalDir)
		}
		slog.Info("phase3: FAIL, retrying", "round", round+1)
	}

	return fmt.Errorf("exceeded max rounds for topic: %s", g.Topic)
}

func (g *Generator) phaseGenerate(ctx context.Context, chalDir string, labTools []tool.InvokableTool) error {
	agent, err := claudecode.New(
		claudecode.WithSystemPrompt(WorkerSystemPrompt()),
		claudecode.WithTools("Read", "Write", "Edit", "Bash"),
		claudecode.WithCustomTools(labTools...),
		claudecode.WithCWD(g.workDir),
		claudecode.WithAddDirs(chalDir, g.workDir),
		claudecode.WithPermissionMode("acceptEdits"),
	)
	if err != nil {
		return fmt.Errorf("create worker agent: %w", err)
	}

	prompt := fmt.Sprintf(WorkerPrompt, g.Topic, chalDir)
	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent})
	events := runner.Run(ctx, []adk.Message{schema.UserMessage(prompt)})

	for {
		evt, ok := events.Next()
		if !ok {
			break
		}
		if evt.Err != nil {
			return evt.Err
		}
		if evt.Output != nil && evt.Output.MessageOutput != nil && evt.Output.MessageOutput.Message != nil {
			if c := strings.TrimSpace(evt.Output.MessageOutput.Message.Content); c != "" {
				fmt.Println(c)
			}
		}
		if evt.Action != nil && evt.Action.Exit {
			break
		}
	}
	return nil
}

func (g *Generator) phaseJudge(ctx context.Context, chalDir string) bool {
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
		claudecode.WithCWD(g.workDir),
		claudecode.WithPermissionMode("bypassPermissions"),
	)
	if err != nil {
		slog.Error("create judge agent", "err", err)
		return false
	}

	prompt := fmt.Sprintf(JudgePrompt, g.Topic, fileContents.String())
	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent})
	events := runner.Run(ctx, []adk.Message{schema.UserMessage(prompt)})

	var lastMsg string
	for {
		evt, ok := events.Next()
		if !ok {
			break
		}
		if evt.Err != nil {
			return false
		}
		if evt.Output != nil && evt.Output.MessageOutput != nil && evt.Output.MessageOutput.Message != nil {
			lastMsg = strings.TrimSpace(evt.Output.MessageOutput.Message.Content)
		}
		if evt.Action != nil && evt.Action.Exit {
			break
		}
	}

	return strings.Contains(lastMsg, "PASS") || strings.Contains(lastMsg, `"pass": true`)
}

func (g *Generator) phaseVerify(ctx context.Context, chalDir string) bool {
	imageName := fmt.Sprintf("%s/%s/%s:v1", g.Registry, g.ACRNS, g.challengeID)

	if !BuildAndPush(ctx, imageName, chalDir) {
		return false
	}

	k8sClient, err := k8s.New(g.Kubeconfig)
	if err != nil {
		slog.Error("k8s client", "err", err)
		return false
	}
	ns := "breakfix-verify"
	podName := "verify-" + g.challengeID

	//nolint:errcheck // best-effort pre-create namespace
	k8sClient.EnsureNamespace(ns)

	if err := k8sClient.CreatePod(ns, podName, k8s.CreatePodOpts{Image: imageName}); err != nil {
		slog.Error("create verify pod", "err", err)
		return false
	}
	//nolint:errcheck // defer cleanup
	defer k8sClient.DeletePod(ns, podName)

	if err := k8sClient.WaitForPod(ns, podName, "challenge"); err != nil {
		slog.Error("wait verify pod", "err", err)
		return false
	}

	answerPath := filepath.Join(chalDir, "answer.sh")
	if err := k8sClient.CopyToPod(ns, podName, answerPath, "/tmp/answer.sh"); err != nil {
		slog.Error("copy answer.sh", "err", err)
		return false
	}
	ansExit, ansOut, err := k8sClient.ExecInPod(ns, podName, "bash", "/tmp/answer.sh")
	if err != nil {
		slog.Error("exec answer.sh", "err", err)
		return false
	}
	if ansExit != 0 {
		slog.Error("answer.sh failed", "exit", ansExit, "output", ansOut)
		return false
	}

	verifyPath := filepath.Join(chalDir, "verify.sh")
	if err := k8sClient.CopyToPod(ns, podName, verifyPath, "/tmp/verify.sh"); err != nil {
		slog.Error("copy verify.sh", "err", err)
		return false
	}
	exitCode, output, err := k8sClient.ExecInPod(ns, podName, "bash", "/tmp/verify.sh")
	if err != nil {
		slog.Error("exec verify.sh", "err", err)
		return false
	}

	if exitCode == 0 {
		slog.Info("verification PASSED")
		return true
	}
	slog.Error("verification FAILED", "exit", exitCode, "output", output)
	return false
}

func (g *Generator) finalize(chalDir string) error {
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
		//nolint:gosec // challenge files are public
		if err := os.WriteFile(filepath.Join(dst, e.Name()), data, 0644); err != nil {
			slog.Error("failed to write output file", "err", err, "name", e.Name())
		}
	}

	fmt.Println(g.challengeID)
	slog.Info("challenge finalized", "id", g.challengeID, "dir", dst)
	return nil
}

func sanitizeID(topic string) string {
	words := strings.Fields(strings.ToLower(topic))
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
		h := fmt.Sprintf("%x", sum([]byte(topic)))
		id = "challenge-" + h[:8]
	}
	if len(id) > 50 {
		id = id[:50]
	}
	return strings.Trim(id, "-")
}

func sum(b []byte) [16]byte {
	var h [16]byte
	for i, c := range b {
		h[i%16] ^= c + byte(i)
	}
	return h
}
