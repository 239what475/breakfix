package k8s

import (
	"context"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	k8sErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type CreateJobOpts struct {
	Image                 string
	Env                   map[string]string
	EnvSecretName         string
	ImagePullPolicy       corev1.PullPolicy
	ActiveDeadlineSeconds *int64
}

// CreateJob creates a K8s Job. Returns the created Job name.
func (c *Client) CreateJob(ns, jobName string, opts CreateJobOpts) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	envVars := make([]corev1.EnvVar, 0, len(opts.Env))
	for k, v := range opts.Env {
		envVars = append(envVars, corev1.EnvVar{Name: k, Value: v})
	}
	envFrom := []corev1.EnvFromSource(nil)
	if opts.EnvSecretName != "" {
		envFrom = append(envFrom, corev1.EnvFromSource{
			SecretRef: &corev1.SecretEnvSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: opts.EnvSecretName},
			},
		})
	}
	pullPolicy := opts.ImagePullPolicy
	if pullPolicy == "" {
		pullPolicy = corev1.PullAlways
	}

	backoff := int32(0)
	ttl := int32(3600) // keep Job for 1h for log access
	activeDeadline := opts.ActiveDeadlineSeconds
	if activeDeadline == nil {
		activeDeadline = ptr(int64(3600)) // cap runtime at 1h
	}

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: jobName, Namespace: ns},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoff,
			ActiveDeadlineSeconds:   activeDeadline,
			TTLSecondsAfterFinished: &ttl,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:            "generator",
						Image:           opts.Image,
						ImagePullPolicy: pullPolicy,
						SecurityContext: &corev1.SecurityContext{Privileged: ptr(true)},
						Env:             envVars,
						EnvFrom:         envFrom,
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

func (c *Client) DeleteJob(ns, jobName string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	propagation := metav1.DeletePropagationForeground
	err := c.clientset.BatchV1().Jobs(ns).Delete(ctx, jobName, metav1.DeleteOptions{
		PropagationPolicy: &propagation,
	})
	if k8sErrors.IsNotFound(err) {
		return nil
	}
	return err
}
