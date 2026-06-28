package k8s

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// CreatePodOpts holds the configurable fields for CreatePod.
type CreatePodOpts struct {
	Image           string
	Command         []string
	ChallengeID     string
	InstanceID      string
	ImagePullPolicy corev1.PullPolicy
}

// CreatePod creates a pod in the given namespace and returns an error on failure.
func (c *Client) CreatePod(namespace, podName string, opts CreatePodOpts) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if len(opts.Command) == 0 {
		opts.Command = []string{"sleep", "infinity"}
	}
	if opts.ImagePullPolicy == "" {
		opts.ImagePullPolicy = corev1.PullIfNotPresent
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
			Containers: []corev1.Container{{
				Name:            "challenge",
				Image:           opts.Image,
				Command:         opts.Command,
				ImagePullPolicy: opts.ImagePullPolicy,
			}},
			RestartPolicy: corev1.RestartPolicyNever,
		},
	}

	if opts.ChallengeID != "" {
		pod.Labels["challenge-id"] = opts.ChallengeID
	}
	if opts.InstanceID != "" {
		pod.Labels["instance-id"] = opts.InstanceID
	}

	_, err := c.clientset.CoreV1().Pods(namespace).Create(ctx, pod, metav1.CreateOptions{})
	return err
}

// WaitForPod polls until the named container is ready.
func (c *Client) WaitForPod(namespace, podName, containerName string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
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
			return fmt.Errorf("pod %s entered %s state", podName, pod.Status.Phase)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for pod %s", podName)
		case <-time.After(500 * time.Millisecond):
		}
	}
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
