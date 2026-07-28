package k8s

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"k8s.io/client-go/tools/remotecommand"
)

func TestLimitedBufferRejectsOutputBeyondLimit(t *testing.T) {
	buffer := &limitedBuffer{limit: 5}
	if _, err := buffer.Write([]byte("hello")); err != nil {
		t.Fatalf("write within limit: %v", err)
	}
	if _, err := buffer.Write([]byte("!")); !errors.Is(err, ErrExecOutputLimit) {
		t.Fatalf("expected output limit error, got %v", err)
	}
	if got := buffer.String(); got != "hello" {
		t.Fatalf("unexpected retained output %q", got)
	}
}

func TestJoinExecOutputPreservesStdoutBeforeStderr(t *testing.T) {
	if got := joinExecOutput("{\"checks\":[]}", "warning"); got != "{\"checks\":[]}\nwarning" {
		t.Fatalf("unexpected combined output %q", got)
	}
	if got := joinExecOutput("", "warning"); got != "warning" {
		t.Fatalf("unexpected stderr-only output %q", got)
	}
}

type blockingPTYExecutor struct {
	contextSeen chan context.Context
}

func (e *blockingPTYExecutor) StreamWithContext(ctx context.Context, _ remotecommand.StreamOptions) error {
	e.contextSeen <- ctx
	<-ctx.Done()
	return ctx.Err()
}

func TestStreamPTYPropagatesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	executor := &blockingPTYExecutor{contextSeen: make(chan context.Context, 1)}
	done := make(chan error, 1)
	go func() { done <- streamPTY(ctx, executor, remotecommand.StreamOptions{}) }()
	select {
	case seen := <-executor.contextSeen:
		if seen != ctx {
			t.Fatal("stream did not receive the caller context")
		}
	case <-time.After(time.Second):
		t.Fatal("stream did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("stream result = %v, want context cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("stream was not cancelled")
	}
}

func TestLiveExecInPodStreamsCheckpointJSON(t *testing.T) {
	if os.Getenv("RUN_LIVE_K8S_EXEC") != "1" {
		t.Skip("set RUN_LIVE_K8S_EXEC=1 to run against a real Kubernetes pod")
	}

	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Fatal(err)
		}
		kubeconfig = filepath.Join(home, ".kube", "config")
	}
	namespace := os.Getenv("BREAKFIX_LIVE_EXEC_NAMESPACE")
	pod := os.Getenv("BREAKFIX_LIVE_EXEC_POD")
	if namespace == "" || pod == "" {
		t.Fatal("BREAKFIX_LIVE_EXEC_NAMESPACE and BREAKFIX_LIVE_EXEC_POD are required")
	}

	client, err := New(kubeconfig)
	if err != nil {
		t.Fatalf("new Kubernetes client: %v", err)
	}
	exitCode, output, err := client.ExecInPod(namespace, pod, "/checks/checkpoints.sh", "--json")
	if err != nil {
		t.Fatalf("exec checkpoints: %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("checkpoint command exit=%d output=%q", exitCode, output)
	}
	if !json.Valid([]byte(output)) {
		t.Fatalf("checkpoint output is not JSON: %q", output)
	}
}
