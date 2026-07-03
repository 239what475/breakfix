package k8s

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
	k8sexec "k8s.io/client-go/util/exec"
)

// ExecInPod runs a command in a pod and returns exit code, stdout+stderr, and any error.
func (c *Client) ExecInPod(namespace, podName string, command ...string) (int, string, error) {
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

	var stdout, stderr bytes.Buffer
	err = exec.StreamWithContext(context.Background(), remotecommand.StreamOptions{Stdout: &stdout, Stderr: &stderr})
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(k8sexec.CodeExitError); ok {
			exitCode = exitErr.Code
		} else {
			return -1, "", fmt.Errorf("exec: %w", err)
		}
	}
	output := stdout.String()
	if stderr.Len() > 0 {
		if output != "" {
			output += "\n"
		}
		output += stderr.String()
	}
	return exitCode, strings.TrimSpace(output), nil
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

// ExecPTY opens a PTY session via client-go remotecommand.
func (c *Client) ExecPTY(stdin io.Reader, stdout, stderr io.Writer, resize <-chan remotecommand.TerminalSize, namespace, podName, sessionName string) error {
	req := c.clientset.CoreV1().RESTClient().Post().
		Resource("pods").Name(podName).Namespace(namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Command: []string{
				"/bin/bash",
				"-lc",
				fmt.Sprintf("export TERM=xterm-256color; tmux new-session -A -s %s", shellQuote(sessionName)),
			},
			Stdin:   true,
			Stdout:  true,
			Stderr:  true,
			TTY:     true,
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
