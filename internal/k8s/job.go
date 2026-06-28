package k8s

import (
	"context"
	"fmt"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// CreateJob creates a K8s Job with the given image and environment.
func (c *Client) CreateJob(namespace, jobName, image string, env map[string]string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	envVars := make([]corev1.EnvVar, 0, len(env))
	for k, v := range env {
		envVars = append(envVars, corev1.EnvVar{Name: k, Value: v})
	}

	backoff := int32(0)
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: jobName, Namespace: namespace},
		Spec: batchv1.JobSpec{
			BackoffLimit: &backoff,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:            "generator",
						Image:           image,
						ImagePullPolicy: corev1.PullIfNotPresent,
						Env:             envVars,
					}},
					ServiceAccountName: "breakfix-generator",
					RestartPolicy:      corev1.RestartPolicyNever,
				},
			},
		},
	}
	_, err := c.clientset.BatchV1().Jobs(namespace).Create(ctx, job, metav1.CreateOptions{})
	return err
}

// WaitForJob polls until the Job completes or fails, returning the pod name on success.
func (c *Client) WaitForJob(namespace, jobName string, timeout time.Duration) (succeeded bool, podName string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	for {
		job, err := c.clientset.BatchV1().Jobs(namespace).Get(ctx, jobName, metav1.GetOptions{})
		if err != nil {
			return false, "", err
		}
		if job.Status.Succeeded > 0 {
			pn := c.jobPodName(namespace, jobName)
			return true, pn, nil
		}
		if job.Status.Failed > 0 {
			pn := c.jobPodName(namespace, jobName)
			return false, pn, fmt.Errorf("job %s failed", jobName)
		}
		select {
		case <-ctx.Done():
			return false, "", fmt.Errorf("timeout waiting for job %s", jobName)
		case <-time.After(2 * time.Second):
		}
	}
}

func (c *Client) jobPodName(namespace, jobName string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pods, _ := c.clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("job-name=%s", jobName),
	})
	if len(pods.Items) > 0 {
		return pods.Items[0].Name
	}
	return ""
}

// ReadPodLogs returns the pod's log output.
func (c *Client) ReadPodLogs(namespace, podName string) (string, error) {
	logs, err := c.clientset.CoreV1().Pods(namespace).GetLogs(podName, &corev1.PodLogOptions{}).Do(context.Background()).Raw()
	if err != nil {
		return "", err
	}
	return string(logs), nil
}
