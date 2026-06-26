package k8s

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/remotecommand"
)

type Client struct {
	clientset  *kubernetes.Clientset
	restConfig *rest.Config
}

func New(kubeconfig string) (*Client, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig != "" {
		loadingRules.ExplicitPath = kubeconfig
	}
	configOverrides := &clientcmd.ConfigOverrides{}
	clientConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)

	config, err := clientConfig.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("build kubeconfig: %w", err)
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("create clientset: %w", err)
	}
	return &Client{clientset: clientset, restConfig: config}, nil
}

// ── Namespace ──

func (c *Client) EnsureNamespace(name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := c.clientset.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		return nil // already exists
	}

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: name},
	}
	_, err = c.clientset.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
	return err
}

func (c *Client) DeleteNamespace(name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	err := c.clientset.CoreV1().Namespaces().Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil {
		// Already gone is fine
		return nil //nolint:nilerr
	}
	return nil
}

// ── Pod ──

func (c *Client) CreatePod(namespace, podName, image, challengeID, instanceID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: namespace,
			Labels: map[string]string{
				"breakfix":     "true",
				"challenge-id": challengeID,
				"instance-id":  instanceID,
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:            "challenge",
					Image:           image,
					Command:         []string{"sleep", "infinity"},
					ImagePullPolicy: corev1.PullIfNotPresent,
				},
			},
			RestartPolicy: corev1.RestartPolicyNever,
		},
	}

	_, err := c.clientset.CoreV1().Pods(namespace).Create(ctx, pod, metav1.CreateOptions{})
	return err
}

func (c *Client) WaitForPod(namespace, podName string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	for {
		pod, err := c.clientset.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if pod.Status.Phase == corev1.PodRunning {
			for _, cs := range pod.Status.ContainerStatuses {
				if cs.Name == "challenge" && cs.Ready {
					return nil
				}
			}
		}
		if pod.Status.Phase == corev1.PodFailed || pod.Status.Phase == corev1.PodSucceeded {
			return fmt.Errorf("pod %s entered %s state", podName, pod.Status.Phase)
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for pod %s", podName)
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func (c *Client) DeletePod(namespace, podName string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := c.clientset.CoreV1().Pods(namespace).Delete(ctx, podName, metav1.DeleteOptions{})
	if err != nil {
		return nil //nolint:nilerr // already gone is fine
	}
	return nil
}


// ── Exec (non-interactive) ──

func (c *Client) ExecInPod(namespace, podName string, command ...string) (int, string, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.Command("kubectl", append(
		[]string{"exec", "-n", namespace, podName, "--"},
		command...,
	)...) //nolint:gosec
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
	cmd := exec.Command("kubectl", "cp", localPath,
		fmt.Sprintf("%s/%s:%s", namespace, podName, remotePath)) //nolint:gosec
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// ── Interactive SSH ──


// ── Helpers ──

func RandomID() string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 6)
	for i := range b {
		b[i] = chars[rand.Intn(len(chars))] //nolint:gosec
	}
	return string(b)
}

func UserNamespace(userID string) string {
	return "breakfix-" + strings.ReplaceAll(userID, "_", "-")
}

func VerifyScriptPath(challengeDir string) string {
	return filepath.Join(challengeDir, "verify.sh")
}


// ExecPTY opens a PTY session in a pod via client-go remotecommand.
func (c *Client) ExecPTY(stdin io.Reader, stdout, stderr io.Writer, namespace, podName string) error {
	req := c.clientset.CoreV1().RESTClient().Post().
		Resource("pods").Name(podName).Namespace(namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Command: []string{"/bin/bash"},
			Stdin:   true,
			Stdout:  true,
			Stderr:  true,
			TTY:     true,
		}, scheme.ParameterCodec)

	exec, err := remotecommand.NewSPDYExecutor(c.restConfig, "POST", req.URL())
	if err != nil {
		return fmt.Errorf("exec: %w", err)
	}

	return exec.Stream(remotecommand.StreamOptions{
		Stdin:  stdin,
		Stdout: stdout,
		Stderr: stderr,
		Tty:    true,
	})
}
