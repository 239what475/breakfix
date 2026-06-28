package generator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	claudecode "github.com/239what475/eino-claude-code"
	"k8s.io/klog/v2"

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
	os.RemoveAll(g.workDir)
	g.challengeID = sanitizeID(g.Topic)
	chalDir := filepath.Join(g.workDir, g.challengeID)
	os.MkdirAll(chalDir, 0755)

	klog.InfoS("generator started", "topic", g.Topic, "challengeID", g.challengeID)

	// Create K8s client for MCP tools
	k8sClient, err := k8s.New(g.Kubeconfig)
	if err != nil {
		return fmt.Errorf("k8s client: %w", err)
	}
	labClient := NewLabClient(k8sClient, "breakfix-gen")
	labTools := labClient.Tools()

	for round := 0; round < 5; round++ {
		klog.InfoS("round", "n", round+1)

		// Phase 1: Worker — generate challenge files with lab testing
		if err := g.phaseGenerate(ctx, chalDir, labTools); err != nil {
			klog.ErrorS(err, "phase1 failed", "round", round+1)
			continue
		}

		// Phase 2: Judge — review files in isolated session
		if !g.phaseJudge(ctx, chalDir) {
			klog.InfoS("phase2: FAIL, retrying", "round", round+1)
			continue
		}
		klog.InfoS("phase2: PASS")

		// Phase 3: Verify — build image + full test
		if g.phaseVerify(ctx, chalDir) {
			return g.finalize(chalDir)
		}
		klog.InfoS("phase3: FAIL, retrying", "round", round+1)
	}

	return fmt.Errorf("exceeded max rounds for topic: %s", g.Topic)
}

// phaseGenerate runs the Worker agent (Claude Code with MCP tools).
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

// phaseJudge runs the Judge agent (isolated session, read-only).
func (g *Generator) phaseJudge(ctx context.Context, chalDir string) bool {
	var fileContents strings.Builder
	entries, _ := os.ReadDir(chalDir)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, _ := os.ReadFile(filepath.Join(chalDir, e.Name()))
		fileContents.WriteString(fmt.Sprintf("\n--- %s ---\n%s\n", e.Name(), string(data)))
	}

	agent, err := claudecode.New(
		claudecode.WithSystemPrompt(JudgeSystemPrompt()),
		claudecode.WithTools("Read"),
		claudecode.WithCWD(g.workDir),
		claudecode.WithPermissionMode("bypassPermissions"),
	)
	if err != nil {
		klog.ErrorS(err, "create judge agent")
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

// phaseVerify builds the image and runs verification in K8s.
func (g *Generator) phaseVerify(ctx context.Context, chalDir string) bool {
	imageName := fmt.Sprintf("%s/%s/%s:v1", g.Registry, g.ACRNS, g.challengeID)

	if !BuildAndPush(ctx, imageName, chalDir) {
		return false
	}

	k8sClient, _ := k8s.New(g.Kubeconfig)
	ns := "breakfix-verify"
	podName := "verify-" + g.challengeID

	k8sClient.EnsureNamespace(ns)

	if err := k8sClient.CreatePod(ns, podName, imageName, "", ""); err != nil {
		fmt.Fprintf(os.Stderr, "create pod: %v\n", err)
		return false
	}
	defer k8sClient.DeletePod(ns, podName)

	if err := k8sClient.WaitForPod(ns, podName); err != nil {
		fmt.Fprintf(os.Stderr, "wait pod: %v\n", err)
		return false
	}

	// Copy answer.sh to pod and execute
	answerPath := filepath.Join(chalDir, "answer.sh")
	if err := k8sClient.CopyToPod(ns, podName, answerPath, "/tmp/answer.sh"); err != nil {
		fmt.Fprintf(os.Stderr, "copy answer.sh: %v\n", err)
		return false
	}
	ansExit, ansOut, _ := k8sClient.ExecInPod(ns, podName, "bash", "/tmp/answer.sh")
	if ansExit != 0 {
		fmt.Fprintf(os.Stderr, "answer.sh failed (exit=%d): %s\n", ansExit, ansOut)
		return false
	}

	// Copy verify.sh to pod and execute
	verifyPath := filepath.Join(chalDir, "verify.sh")
	if err := k8sClient.CopyToPod(ns, podName, verifyPath, "/tmp/verify.sh"); err != nil {
		fmt.Fprintf(os.Stderr, "copy verify.sh: %v\n", err)
		return false
	}
	exitCode, output, _ := k8sClient.ExecInPod(ns, podName, "bash", "/tmp/verify.sh")

	if exitCode == 0 {
		fmt.Printf("  ✓ Verification PASSED\n")
		return true
	}
	fmt.Fprintf(os.Stderr, "  ✗ Verification FAILED (exit=%d): %s\n", exitCode, output)
	return false
}

// finalize copies generated files to the output directory.
func (g *Generator) finalize(chalDir string) error {
	dst := filepath.Join(g.OutputDir, g.challengeID)
	os.RemoveAll(dst)
	os.MkdirAll(dst, 0755)

	entries, _ := os.ReadDir(chalDir)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, _ := os.ReadFile(filepath.Join(chalDir, e.Name()))
		os.WriteFile(filepath.Join(dst, e.Name()), data, 0644)
	}

	fmt.Println(g.challengeID)
	klog.InfoS("challenge finalized", "id", g.challengeID, "dir", dst)
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
