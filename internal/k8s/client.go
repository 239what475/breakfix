package k8s

import (
	"bytes"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/breakfix/breakfix/internal/build"
)

type Client struct {
	kubeconfig string
}

func New(kubeconfig string) *Client {
	return &Client{kubeconfig: kubeconfig}
}

func (c *Client) kubectl(args ...string) *exec.Cmd {
	cmd := exec.Command("kubectl", args...)
	if c.kubeconfig != "" && !build.IsDev() {
		cmd.Args = append(cmd.Args, "--kubeconfig", c.kubeconfig)
	}
	return cmd
}

func (c *Client) EnsureNamespace(name string) error {
	cmd := c.kubectl("create", "namespace", name)
	cmd.Stderr = os.Stderr
	// Ignore error if namespace already exists
	_ = cmd.Run()
	return nil
}

func (c *Client) DeleteNamespace(name string) error {
	cmd := c.kubectl("delete", "namespace", name, "--ignore-not-found")
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (c *Client) CreatePod(namespace, podName, image, challengeID, instanceID string) error {
	podYAML := fmt.Sprintf(`apiVersion: v1
kind: Pod
metadata:
  name: %s
  namespace: %s
  labels:
    breakfix: "true"
    challenge-id: "%s"
    instance-id: "%s"
spec:
  containers:
  - name: challenge
    image: %s
    command: ["sleep", "infinity"]
    imagePullPolicy: IfNotPresent
  restartPolicy: Never
`, podName, namespace, challengeID, instanceID, image)

	cmd := c.kubectl("apply", "-f", "-")
	cmd.Stdin = strings.NewReader(podYAML)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("create pod: %w: %s", err, stderr.String())
	}
	return nil
}

func (c *Client) WaitForPod(namespace, podName string) error {
	cmd := c.kubectl("wait", "--for=condition=Ready",
		"-n", namespace,
		fmt.Sprintf("pod/%s", podName),
		"--timeout=60s")
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (c *Client) DeletePod(namespace, podName string) error {
	cmd := c.kubectl("delete", "pod", podName, "-n", namespace, "--ignore-not-found")
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (c *Client) ExecInPod(namespace, podName string, command ...string) (int, string, error) {
	args := []string{"exec", "-n", namespace, podName, "--"}
	args = append(args, command...)
	cmd := c.kubectl(args...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
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

func (c *Client) CopyToPod(namespace, podName, localPath, remotePath string) error {
	cmd := c.kubectl("cp", localPath, fmt.Sprintf("%s/%s:%s", namespace, podName, remotePath))
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func UserNamespace(userID string) string {
	return "breakfix-" + strings.ReplaceAll(userID, "_", "-")
}

func RandomID() string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 6)
	for i := range b {
		b[i] = chars[rand.Intn(len(chars))] //nolint:gosec
	}
	return string(b)
}

// ExecSSH provides an interactive terminal session via kubectl exec
func ExecSSH(namespace, podName string) error {
	cmd := exec.Command("kubectl", "exec", "-ti", "-n", namespace, podName, "--", "/bin/bash")
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func VerifyScriptPath(challengeDir string) string {
	return filepath.Join(challengeDir, "verify.sh")
}
