package generator

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/k8s"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	corev1 "k8s.io/api/core/v1"
)

// LabClient wraps the k8s client for lab pod MCP tools.
type LabClient struct {
	K8s          *k8s.Client
	Namespace    string
	WorkDir      string // challenge directory where agent writes files
	RegistryAddr string // same as challenge Dockerfile FROM
}

func NewLabClient(client *k8s.Client, namespace, workDir, registryAddr string) *LabClient {
	return &LabClient{K8s: client, Namespace: namespace, WorkDir: workDir, RegistryAddr: registryAddr}
}

func (c *LabClient) Tools() []tool.InvokableTool {
	return []tool.InvokableTool{
		&labCreateTool{c},
		&labExecTool{c},
		&labCheckpointsTool{c},
		&labLogsTool{c},
		&labDestroyTool{c},
	}
}

// ── labCreate ──

type labCreateTool struct{ lc *LabClient }

func (t *labCreateTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "lab_create",
		Desc: "Create a temporary lab pod (breakfix-base) for testing. Returns the pod name.",
	}, nil
}

func (t *labCreateTool) InvokableRun(ctx context.Context, argsJSON string, _ ...tool.Option) (string, error) {
	start := time.Now()
	ns := t.lc.Namespace

	// Clean up old lab pods
	oldPods, _ := t.lc.K8s.ListPods(ns)
	for _, p := range oldPods {
		if strings.HasPrefix(p, "lab-") {
			t.lc.K8s.DeletePod(ns, p) //nolint:errcheck
		}
	}

	podName := "lab-" + k8s.RandomID()
	if err := t.lc.K8s.EnsureNamespace(ns); err != nil {
		slog.Info("tool done", "tool", "lab_create", "duration", time.Since(start), "err", err)
		return "", fmt.Errorf("ensure namespace: %w", err)
	}
	labImage := t.lc.RegistryAddr + "/breakfix-base:latest"
	if err := t.lc.K8s.CreatePod(ns, podName, k8s.CreatePodOpts{Image: labImage}); err != nil {
		slog.Info("tool done", "tool", "lab_create", "duration", time.Since(start), "err", err)
		return "", fmt.Errorf("create pod: %w", err)
	}
	if err := t.lc.K8s.WaitForPod(ns, podName, "challenge"); err != nil {
		slog.Info("tool done", "tool", "lab_create", "duration", time.Since(start), "err", err)
		return "", fmt.Errorf("wait pod: %w", err)
	}

	slog.Info("tool done", "tool", "lab_create", "pod", podName, "duration", time.Since(start))
	return podName, nil
}

// ── labExec ──

type labExecTool struct{ lc *LabClient }

func (t *labExecTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "lab_exec",
		Desc: "Run a bash script in the lab pod. script: the shell script. pod: pod name.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"pod":    {Type: schema.String, Desc: "Pod name", Required: true},
			"script": {Type: schema.String, Desc: "Bash script to execute", Required: true},
		}),
	}, nil
}

func (t *labExecTool) InvokableRun(ctx context.Context, argsJSON string, _ ...tool.Option) (string, error) {
	start := time.Now()
	var a struct {
		Pod    string `json:"pod"`
		Script string `json:"script"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		slog.Info("tool done", "tool", "lab_exec", "duration", time.Since(start), "err", err)
		return "", fmt.Errorf("parse args: %w", err)
	}
	exitCode, output, err := t.lc.K8s.ExecInPod(t.lc.Namespace, a.Pod, "bash", "-c", a.Script)
	slog.Info("tool done", "tool", "lab_exec", "pod", a.Pod, "exit", exitCode, "duration", time.Since(start), "err", err)
	result := fmt.Sprintf("exit=%d output=%s", exitCode, output)
	if err != nil {
		result += fmt.Sprintf(" error=%v", err)
	}
	return result, nil
}

// ── labCheckpoints ──

type labCheckpointsTool struct{ lc *LabClient }

func (t *labCheckpointsTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "lab_checkpoints",
		Desc: "Copy checks/checkpoints.sh from the workspace to a lab pod and run it with --json. pod: pod name. Returns the checkpoint report.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"pod": {Type: schema.String, Desc: "Pod name", Required: true},
		}),
	}, nil
}

func (t *labCheckpointsTool) InvokableRun(ctx context.Context, argsJSON string, _ ...tool.Option) (string, error) {
	start := time.Now()
	var a struct {
		Pod string `json:"pod"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", err
	}
	data, _ := os.ReadFile(filepath.Join(t.lc.WorkDir, "checks", "checkpoints.sh"))
	script := string(data)
	if script == "" {
		script = "echo checks/checkpoints.sh not found; exit 1"
	}
	// The checker contract is always JSON. Supplying $0 also keeps scripts that
	// inspect positional arguments from seeing an empty program name.
	exitCode, output, err := t.lc.K8s.ExecInPod(
		t.lc.Namespace,
		a.Pod,
		"bash",
		"-c",
		script,
		"breakfix-checkpoints",
		"--json",
	)
	slog.Info("tool done", "tool", "lab_checkpoints", "pod", a.Pod, "exit", exitCode, "duration", time.Since(start), "err", err)
	result := fmt.Sprintf("exit=%d output=%s", exitCode, output)
	if err != nil {
		result += fmt.Sprintf(" error=%v", err)
	}
	return result, nil
}

// ── labLogs ──

type labLogsTool struct{ lc *LabClient }

func (t *labLogsTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "lab_logs",
		Desc: "Get pod logs. pod: pod name.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"pod": {Type: schema.String, Desc: "Pod name", Required: true},
		}),
	}, nil
}

func (t *labLogsTool) InvokableRun(ctx context.Context, argsJSON string, _ ...tool.Option) (string, error) {
	start := time.Now()
	var a struct {
		Pod string `json:"pod"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", err
	}
	logs, err := t.lc.K8s.Clientset().CoreV1().Pods(t.lc.Namespace).GetLogs(a.Pod, &corev1.PodLogOptions{}).Do(ctx).Raw()
	slog.Info("tool done", "tool", "lab_logs", "pod", a.Pod, "duration", time.Since(start), "err", err)
	if err != nil {
		return "", err
	}
	return string(logs), nil
}

// ── labDestroy ──

type labDestroyTool struct{ lc *LabClient }

func (t *labDestroyTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "lab_destroy",
		Desc: "Delete a lab pod. pod: pod name.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"pod": {Type: schema.String, Desc: "Pod name", Required: true},
		}),
	}, nil
}

func (t *labDestroyTool) InvokableRun(ctx context.Context, argsJSON string, _ ...tool.Option) (string, error) {
	start := time.Now()
	var a struct {
		Pod string `json:"pod"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", err
	}
	if err := t.lc.K8s.DeletePod(t.lc.Namespace, a.Pod); err != nil {
		slog.Info("tool done", "tool", "lab_destroy", "pod", a.Pod, "duration", time.Since(start), "err", err)
		return "", err
	}
	slog.Info("tool done", "tool", "lab_destroy", "pod", a.Pod, "duration", time.Since(start))
	return "destroyed", nil
}
