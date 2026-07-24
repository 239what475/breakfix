package k8s

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
	k8sexec "k8s.io/client-go/util/exec"
)

var ErrExecOutputLimit = errors.New("pod exec output exceeded limit")

const tmuxHistoryLimit = 10000

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

// CaptureTMUXPane returns a page of one tmux window's own scrollback buffer.
// It reads terminal state only; it never attaches to or writes into the pane.
func (c *Client) CaptureTMUXPane(ctx context.Context, namespace, podName, sessionName, windowName string, offset, lines int) ([]string, int, error) {
	if offset < 0 || lines < 1 {
		return nil, 0, errors.New("invalid scrollback range")
	}
	target := sessionName + ":" + windowName
	countCommand := `set -o pipefail
tmux capture-pane -p -J -t "$1" -S - -E - | wc -l`
	_, countOutput, err := c.ExecInPodContext(ctx, namespace, podName, 128, "/bin/bash", "-lc", countCommand, "--", target)
	if err != nil {
		return nil, 0, fmt.Errorf("capture tmux scrollback count: %w", err)
	}
	total, err := strconv.Atoi(strings.TrimSpace(countOutput))
	if err != nil {
		return nil, 0, fmt.Errorf("parse tmux scrollback count %q: %w", countOutput, err)
	}
	pageCommand := `set -o pipefail
tmux capture-pane -p -J -t "$1" -S - -E - | tail -n "$(( $2 + $3 ))" | head -n "$3"`
	_, output, err := c.ExecInPodContext(ctx, namespace, podName, 128*1024, "/bin/bash", "-lc", pageCommand, "--", target, strconv.Itoa(offset), strconv.Itoa(lines))
	if err != nil {
		return nil, 0, fmt.Errorf("capture tmux scrollback: %w", err)
	}
	if output == "" {
		return []string{}, total, nil
	}
	return strings.Split(output, "\n"), total, nil
}

// ListPodFiles lists a single directory level inside one workspace Pod. The
// path is intentionally unrestricted within that Pod; the caller already owns
// the environment and this helper does not cross Pod boundaries.
func (c *Client) ListPodFiles(ctx context.Context, namespace, podName, path string, offset, limit int) ([]string, int, error) {
	if offset < 0 || limit < 1 {
		return nil, 0, errors.New("invalid file listing range")
	}
	listCommand := `set -euo pipefail
path="$1"
if [[ ! -d "$path" ]]; then
  echo "not a directory: $path" >&2
  exit 2
fi
find "$path" -mindepth 1 -maxdepth 1 -printf '%f\t%y\t%s\n' | LC_ALL=C sort`
	countCommand := listCommand + " | wc -l"
	_, countOutput, err := c.ExecInPodContext(ctx, namespace, podName, 128, "/bin/bash", "-lc", countCommand, "--", path)
	if err != nil {
		return nil, 0, fmt.Errorf("list environment files count: %w", err)
	}
	total, err := strconv.Atoi(strings.TrimSpace(countOutput))
	if err != nil {
		return nil, 0, fmt.Errorf("parse environment file count %q: %w", countOutput, err)
	}
	pageCommand := listCommand + ` | tail -n "+$(( $2 + 1 ))" | head -n "$3"`
	_, output, err := c.ExecInPodContext(ctx, namespace, podName, 128*1024, "/bin/bash", "-lc", pageCommand, "--", path, strconv.Itoa(offset), strconv.Itoa(limit))
	if err != nil {
		return nil, 0, fmt.Errorf("list environment files: %w", err)
	}
	if output == "" {
		return []string{}, total, nil
	}
	return strings.Split(output, "\n"), total, nil
}

// ReadPodFile reads one bounded byte range from a regular file in a workspace
// Pod. It deliberately permits any Pod-local path but excludes streams and
// device files so an assistant request cannot block indefinitely.
func (c *Client) ReadPodFile(ctx context.Context, namespace, podName, path string, offset int64, maxBytes int) (string, int64, error) {
	if offset < 0 || maxBytes < 1 {
		return "", 0, errors.New("invalid file read range")
	}
	sizeCommand := `set -euo pipefail
path="$1"
if [[ ! -f "$path" ]]; then
  echo "not a regular file: $path" >&2
  exit 2
fi
wc -c < "$path"`
	_, sizeOutput, err := c.ExecInPodContext(ctx, namespace, podName, 128, "/bin/bash", "-lc", sizeCommand, "--", path)
	if err != nil {
		return "", 0, fmt.Errorf("read environment file size: %w", err)
	}
	size, err := strconv.ParseInt(strings.TrimSpace(sizeOutput), 10, 64)
	if err != nil {
		return "", 0, fmt.Errorf("parse environment file size %q: %w", sizeOutput, err)
	}
	readCommand := `set -euo pipefail
dd if="$1" iflag=skip_bytes,count_bytes skip="$2" count="$3" status=none | base64 -w 0`
	encodedMax := base64.StdEncoding.EncodedLen(maxBytes) + 1024
	_, output, err := c.ExecInPodContext(ctx, namespace, podName, encodedMax, "/bin/bash", "-lc", readCommand, "--", path, strconv.FormatInt(offset, 10), strconv.Itoa(maxBytes))
	if err != nil {
		return "", 0, fmt.Errorf("read environment file: %w", err)
	}
	data, err := base64.StdEncoding.DecodeString(output)
	if err != nil {
		return "", 0, fmt.Errorf("decode environment file: %w", err)
	}
	return string(data), size, nil
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
tmux set-option -q -t %[1]s history-limit %[4]d
exec tmux attach-session -t %[3]s`, shellQuote(sessionName), shellQuote(windowName), shellQuote(sessionName+":"+windowName), tmuxHistoryLimit)

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
