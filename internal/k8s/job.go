package k8s

import (
	"context"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// CreateJob creates a K8s Job. Returns the created Job name.
func (c *Client) CreateJob(ns, jobName, image string, env map[string]string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	envVars := make([]corev1.EnvVar, 0, len(env))
	for k, v := range env {
		envVars = append(envVars, corev1.EnvVar{Name: k, Value: v})
	}

	backoff := int32(0)
	ttl := int32(3600) // keep Job for 1h for log access

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: jobName, Namespace: ns},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoff,
			TTLSecondsAfterFinished: &ttl,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:            "generator",
						Image:           image,
						ImagePullPolicy: corev1.PullIfNotPresent,
						SecurityContext: &corev1.SecurityContext{Privileged: ptr(true)},
						Env:             envVars,
					}},
					ServiceAccountName: "breakfix-generator",
					RestartPolicy:      corev1.RestartPolicyNever,
				},
			},
		},
	}

	_, err := c.clientset.BatchV1().Jobs(ns).Create(ctx, job, metav1.CreateOptions{})
	return err
}
