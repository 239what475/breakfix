package k8s

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PodReadinessError distinguishes a permanent container startup failure from
// a transient wait timeout so reconcilers can publish a terminal status.
type PodReadinessError struct {
	message  string
	terminal bool
}

func (e *PodReadinessError) Error() string {
	if e == nil {
		return "pod readiness error"
	}
	return e.message
}

// IsTerminalPodReadinessError reports whether a readiness wait observed a Pod
// state that cannot become ready without recreating or repairing the Pod.
func IsTerminalPodReadinessError(err error) bool {
	var readinessErr *PodReadinessError
	return errors.As(err, &readinessErr) && readinessErr.terminal
}

// CreatePodOpts holds the configurable fields for CreatePod.
type CreatePodOpts struct {
	Image            string
	Command          []string
	ChallengeID      string
	EnvironmentID    string
	ImagePullPolicy  corev1.PullPolicy
	Env              map[string]string
	VolumeMounts     []corev1.VolumeMount
	Volumes          []corev1.Volume
	Resources        corev1.ResourceRequirements
	ImagePullSecrets []corev1.LocalObjectReference
}

// CreatePod creates a pod in the given namespace and returns an error on failure.
func (c *Client) CreatePod(namespace, podName string, opts CreatePodOpts) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if opts.ImagePullPolicy == "" {
		opts.ImagePullPolicy = corev1.PullIfNotPresent
	}

	container := corev1.Container{
		Name:            "challenge",
		Image:           opts.Image,
		ImagePullPolicy: opts.ImagePullPolicy,
		VolumeMounts:    append([]corev1.VolumeMount{}, opts.VolumeMounts...),
		Resources:       opts.Resources,
	}
	if len(opts.Command) > 0 {
		container.Command = opts.Command
	}
	if len(opts.Env) > 0 {
		envVars := make([]corev1.EnvVar, 0, len(opts.Env))
		for k, v := range opts.Env {
			envVars = append(envVars, corev1.EnvVar{Name: k, Value: v})
		}
		container.Env = envVars
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: namespace,
			Labels: map[string]string{
				"breakfix": "true",
			},
		},
		Spec: corev1.PodSpec{
			Containers:                   []corev1.Container{container},
			AutomountServiceAccountToken: ptr(false),
			RestartPolicy:                corev1.RestartPolicyNever,
			Volumes:                      append([]corev1.Volume{}, opts.Volumes...),
			ImagePullSecrets:             append([]corev1.LocalObjectReference{}, opts.ImagePullSecrets...),
		},
	}

	if opts.ChallengeID != "" {
		pod.Labels["challenge-id"] = opts.ChallengeID
	}
	if opts.EnvironmentID != "" {
		pod.Labels["environment-id"] = opts.EnvironmentID
	}

	_, err := c.clientset.CoreV1().Pods(namespace).Create(ctx, pod, metav1.CreateOptions{})
	return err
}

// WaitForPod polls until the named container is ready.
func (c *Client) WaitForPod(namespace, podName, containerName string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	for {
		pod, err := c.clientset.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if pod.Status.Phase == corev1.PodRunning {
			for _, cs := range pod.Status.ContainerStatuses {
				if cs.Name == containerName && cs.Ready {
					return nil
				}
			}
		}
		switch pod.Status.Phase {
		case corev1.PodFailed, corev1.PodSucceeded:
			return &PodReadinessError{
				message:  fmt.Sprintf("pod %s entered %s state: %s", podName, pod.Status.Phase, podFailureSummary(pod, containerName)),
				terminal: true,
			}
		}
		if summary, failed := podContainerFailure(pod, containerName); failed {
			return &PodReadinessError{
				message:  fmt.Sprintf("pod %s failed before ready: %s", podName, summary),
				terminal: true,
			}
		}
		select {
		case <-ctx.Done():
			return &PodReadinessError{
				message: fmt.Sprintf("timeout waiting for pod %s: %s", podName, podFailureSummary(pod, containerName)),
			}
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func podContainerFailure(pod *corev1.Pod, containerName string) (string, bool) {
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Name != containerName {
			continue
		}
		if waiting := cs.State.Waiting; waiting != nil {
			switch waiting.Reason {
			case "CreateContainerConfigError", "CreateContainerError", "CrashLoopBackOff", "ImagePullBackOff", "ErrImagePull", "RunContainerError":
				return formatContainerState(waiting.Reason, waiting.Message), true
			}
		}
		if terminated := cs.State.Terminated; terminated != nil {
			return formatContainerTermination(terminated), true
		}
		if waiting := cs.LastTerminationState.Terminated; waiting != nil {
			return formatContainerTermination(waiting), true
		}
	}
	return "", false
}

func podFailureSummary(pod *corev1.Pod, containerName string) string {
	if summary, failed := podContainerFailure(pod, containerName); failed {
		return summary
	}
	var reasons []string
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Name != containerName {
			continue
		}
		if cs.State.Waiting != nil {
			reasons = append(reasons, formatContainerState(cs.State.Waiting.Reason, cs.State.Waiting.Message))
		}
	}
	if len(reasons) > 0 {
		return strings.Join(reasons, "; ")
	}
	if strings.TrimSpace(pod.Status.Message) != "" {
		return strings.TrimSpace(pod.Status.Message)
	}
	if strings.TrimSpace(pod.Status.Reason) != "" {
		return strings.TrimSpace(pod.Status.Reason)
	}
	return "no detailed pod status available"
}

func formatContainerState(reason, message string) string {
	if strings.TrimSpace(message) == "" {
		return reason
	}
	return fmt.Sprintf("%s: %s", reason, strings.TrimSpace(message))
}

func formatContainerTermination(state *corev1.ContainerStateTerminated) string {
	if state == nil {
		return "terminated"
	}
	base := fmt.Sprintf("terminated with exit code %d", state.ExitCode)
	if strings.TrimSpace(state.Reason) != "" {
		base = fmt.Sprintf("%s (%s)", base, strings.TrimSpace(state.Reason))
	}
	if strings.TrimSpace(state.Message) != "" {
		base += ": " + strings.TrimSpace(state.Message)
	}
	return base
}

// ListPods returns pod names in a namespace.
func (c *Client) ListPods(namespace string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pods, err := c.clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var names []string
	for _, p := range pods.Items {
		names = append(names, p.Name)
	}
	return names, nil
}

// GetPod returns a pod by name.
func (c *Client) GetPod(namespace, podName string) (*corev1.Pod, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return c.clientset.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
}

// DeletePod deletes a pod, ignoring NotFound errors.
func (c *Client) DeletePod(namespace, podName string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.clientset.CoreV1().Pods(namespace).Delete(ctx, podName, metav1.DeleteOptions{}); err != nil {
		if !isNotFound(err) {
			return err
		}
	}
	return nil
}
