package k8s

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
	k8sexec "k8s.io/client-go/util/exec"
)

var ErrExecOutputLimit = errors.New("pod exec output exceeded limit")

// ExecInPod runs a command in a pod and returns exit code, stdout+stderr, and any error.
func (c *Client) ExecInPod(namespace, podName string, command ...string) (int, string, error) {
	return c.ExecInPodContext(context.Background(), namespace, podName, 0, command...)
}

// ExecInPodContext runs a command with a caller-controlled deadline. maxOutput
// bounds combined stdout and stderr; zero leaves output unbounded.
func (c *Client) ExecInPodContext(ctx context.Context, namespace, podName string, maxOutput int, command ...string) (int, string, error) {
	req := c.clientset.CoreV1().RESTClient().Post().
		Resource("pods").Name(podName).Namespace(namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Command: command,
			Stdout:  true,
			Stderr:  true,
		}, scheme.ParameterCodec)

	exec, err := remotecommand.NewSPDYExecutor(c.restConfig, "POST", req.URL())
	if err != nil {
		return -1, "", fmt.Errorf("exec: %w", err)
	}

	// SPDY transports stdout and stderr on independent streams. Keep separate
	// writers rather than handing the executor one shared buffer; controller
	// reconciles otherwise intermittently observed an empty checkpoint stdout.
	stdout := &limitedBuffer{limit: maxOutput}
	stderr := &limitedBuffer{limit: maxOutput}
	err = exec.StreamWithContext(ctx, remotecommand.StreamOptions{Stdout: stdout, Stderr: stderr})
	output := joinExecOutput(stdout.String(), stderr.String())
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(k8sexec.CodeExitError); ok {
			exitCode = exitErr.Code
		} else if errors.Is(err, ErrExecOutputLimit) {
			return -1, output, ErrExecOutputLimit
		} else {
			return -1, "", fmt.Errorf("exec: %w", err)
		}
	}
	return exitCode, output, nil
}

func joinExecOutput(stdout, stderr string) string {
	stdout = strings.TrimSpace(stdout)
	stderr = strings.TrimSpace(stderr)
	if stdout == "" {
		return stderr
	}
	if stderr == "" {
		return stdout
	}
	return stdout + "\n" + stderr
}

type limitedBuffer struct {
	bytes.Buffer
	limit int
	mu    sync.Mutex
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.limit > 0 && b.Len()+len(p) > b.limit {
		remaining := b.limit - b.Len()
		if remaining > 0 {
			_, _ = b.Buffer.Write(p[:remaining])
		}
		return 0, ErrExecOutputLimit
	}
	return b.Buffer.Write(p)
}

func (b *limitedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.String()
}

// WaitForFileInPod polls until the target file exists inside the pod.
func (c *Client) WaitForFileInPod(namespace, podName, path string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	checkCmd := fmt.Sprintf("test -f %q", path)
	for {
		exitCode, output, err := c.ExecInPod(namespace, podName, "bash", "-lc", checkCmd)
		if err == nil && exitCode == 0 {
			return nil
		}
		if exitCode != 0 && strings.TrimSpace(output) == "" {
			output = "file not present yet"
		}
		select {
		case <-ctx.Done():
			if err != nil {
				return fmt.Errorf("timeout waiting for file %s in pod %s: %w", path, podName, err)
			}
			return fmt.Errorf("timeout waiting for file %s in pod %s: %s", path, podName, strings.TrimSpace(output))
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// ExecPTY opens a named tmux window through a PTY session. The tmux session
// remains in the workspace Pod, so reconnecting a browser tab preserves shell
// state and opening another tab never creates another user environment.
func (c *Client) ExecPTY(stdin io.Reader, stdout, stderr io.Writer, resize <-chan remotecommand.TerminalSize, namespace, podName, sessionName, windowName string) error {
	command := fmt.Sprintf(`export TERM=xterm-256color
if ! tmux has-session -t %[1]s 2>/dev/null; then
  tmux new-session -d -s %[1]s -n %[2]s
elif ! tmux list-windows -t %[1]s -F '#W' | grep -Fx -- %[2]s >/dev/null; then
  tmux new-window -d -t %[1]s -n %[2]s
fi
exec tmux attach-session -t %[3]s`, shellQuote(sessionName), shellQuote(windowName), shellQuote(sessionName+":"+windowName))

	req := c.clientset.CoreV1().RESTClient().Post().
		Resource("pods").Name(podName).Namespace(namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Command: []string{
				"/bin/bash",
				"-lc",
				command,
			},
			Stdin:  true,
			Stdout: true,
			Stderr: true,
			TTY:    true,
		}, scheme.ParameterCodec)

	exec, err := remotecommand.NewSPDYExecutor(c.restConfig, "POST", req.URL())
	if err != nil {
		return fmt.Errorf("exec: %w", err)
	}

	return exec.StreamWithContext(context.Background(), remotecommand.StreamOptions{
		Stdin:             stdin,
		Stdout:            stdout,
		Stderr:            stderr,
		Tty:               true,
		TerminalSizeQueue: &sizeQueue{ch: resize},
	})
}

// ClosePTYWindow closes one named tmux window without affecting the session or
// the workspace Pod. A missing window is treated as already closed.
func (c *Client) ClosePTYWindow(namespace, podName, sessionName, windowName string) error {
	exitCode, output, err := c.ExecInPod(namespace, podName, "/bin/bash", "-lc", fmt.Sprintf(
		"tmux kill-window -t %s 2>/dev/null || true", shellQuote(sessionName+":"+windowName),
	))
	if err != nil {
		return err
	}
	if exitCode != 0 {
		return fmt.Errorf("close tmux window: %s", output)
	}
	return nil
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

type sizeQueue struct {
	ch <-chan remotecommand.TerminalSize
}

func (q *sizeQueue) Next() *remotecommand.TerminalSize {
	s, ok := <-q.ch
	if !ok {
		return nil
	}
	return &s
}
