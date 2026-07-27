package k8s

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestNewJobAppliesVerifierExecutionBoundary(t *testing.T) {
	privileged := true
	backoffLimit := int32(2)
	job := newJob("breakfix-system", "verifier-vt-123", CreateJobOpts{
		Image:              "registry.example/breakfix-verifier:latest",
		ContainerName:      "verifier",
		ServiceAccountName: "breakfix-verifier",
		Privileged:         &privileged,
		BackoffLimit:       &backoffLimit,
		Env:                map[string]string{"Z": "last", "A": "first"},
		ImagePullSecrets:   []corev1.LocalObjectReference{{Name: "breakfix-registry-pull"}},
	})

	if job.Spec.BackoffLimit == nil || *job.Spec.BackoffLimit != 2 {
		t.Fatalf("backoff limit = %v, want 2", job.Spec.BackoffLimit)
	}
	if job.Spec.ActiveDeadlineSeconds == nil || *job.Spec.ActiveDeadlineSeconds != 3600 {
		t.Fatalf("active deadline = %v, want 3600", job.Spec.ActiveDeadlineSeconds)
	}
	pod := job.Spec.Template.Spec
	if pod.ServiceAccountName != "breakfix-verifier" {
		t.Fatalf("service account = %q", pod.ServiceAccountName)
	}
	if len(pod.Containers) != 1 || pod.Containers[0].Name != "verifier" {
		t.Fatalf("containers = %#v", pod.Containers)
	}
	if pod.Containers[0].SecurityContext == nil || pod.Containers[0].SecurityContext.Privileged == nil || !*pod.Containers[0].SecurityContext.Privileged {
		t.Fatalf("rootful BuildKit verifier must be explicitly privileged: %#v", pod.Containers[0].SecurityContext)
	}
	if pod.Containers[0].ImagePullPolicy != corev1.PullAlways {
		t.Fatalf("image pull policy = %q", pod.Containers[0].ImagePullPolicy)
	}
	if got := pod.Containers[0].Env; len(got) != 2 || got[0].Name != "A" || got[1].Name != "Z" {
		t.Fatalf("env = %#v, want sorted keys", got)
	}
	if got := pod.ImagePullSecrets; len(got) != 1 || got[0].Name != "breakfix-registry-pull" {
		t.Fatalf("image pull secrets = %#v", got)
	}
}
