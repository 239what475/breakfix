package k8s

import (
	"context"
	"sort"
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
	BackoffLimit          *int32
	ContainerName         string
	ServiceAccountName    string
	Privileged            *bool
	Labels                map[string]string
	OwnerReferences       []metav1.OwnerReference
}

// CreateJob creates a K8s Job. Returns the created Job name.
func (c *Client) CreateJob(ns, jobName string, opts CreateJobOpts) error {
	_, _, err := c.CreateOrGetJob(ns, jobName, opts)
	return err
}

// CreateOrGetJob atomically converges a deterministically named Job. A
// reconciler can call it repeatedly without creating duplicate executions.
func (c *Client) CreateOrGetJob(ns, jobName string, opts CreateJobOpts) (*batchv1.Job, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	jobs := c.clientset.BatchV1().Jobs(ns)
	if existing, err := jobs.Get(ctx, jobName, metav1.GetOptions{}); err == nil {
		return existing, false, nil
	} else if !k8sErrors.IsNotFound(err) {
		return nil, false, err
	}

	job := newJob(ns, jobName, opts)
	created, err := jobs.Create(ctx, job, metav1.CreateOptions{})
	if k8sErrors.IsAlreadyExists(err) {
		existing, getErr := jobs.Get(ctx, jobName, metav1.GetOptions{})
		if getErr != nil {
			return nil, false, getErr
		}
		return existing, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return created, true, nil
}

func newJob(ns, jobName string, opts CreateJobOpts) *batchv1.Job {
	envVars := make([]corev1.EnvVar, 0, len(opts.Env))
	keys := make([]string, 0, len(opts.Env))
	for key := range opts.Env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		envVars = append(envVars, corev1.EnvVar{Name: key, Value: opts.Env[key]})
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
	if opts.BackoffLimit != nil {
		backoff = *opts.BackoffLimit
	}
	ttl := int32(3600) // keep Job for 1h for log access
	activeDeadline := opts.ActiveDeadlineSeconds
	if activeDeadline == nil {
		activeDeadline = ptr(int64(3600)) // cap runtime at 1h
	}

	containerName := opts.ContainerName
	if containerName == "" {
		containerName = "generator"
	}
	privileged := true
	if opts.Privileged != nil {
		privileged = *opts.Privileged
	}

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:            jobName,
			Namespace:       ns,
			Labels:          opts.Labels,
			OwnerReferences: opts.OwnerReferences,
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoff,
			ActiveDeadlineSeconds:   activeDeadline,
			TTLSecondsAfterFinished: &ttl,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:            containerName,
						Image:           opts.Image,
						ImagePullPolicy: pullPolicy,
						SecurityContext: &corev1.SecurityContext{Privileged: &privileged},
						Env:             envVars,
						EnvFrom:         envFrom,
					}},
					ServiceAccountName: opts.ServiceAccountName,
					RestartPolicy:      corev1.RestartPolicyNever,
				},
			},
		},
	}
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
