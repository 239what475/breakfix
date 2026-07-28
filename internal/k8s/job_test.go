package k8s

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	k8sptr "k8s.io/utils/ptr"
)

func TestNewJobCanEnforceUntrustedBuildBoundary(t *testing.T) {
	privileged := false
	automount := false
	job := newJob("breakfix-system", "builder-vt-123", CreateJobOpts{
		Image:                        "public.example/breakfix-builder:latest",
		ContainerName:                "builder",
		Privileged:                   &privileged,
		AutomountServiceAccountToken: &automount,
		Env:                          map[string]string{"VERIFY_UPLOAD_GRANT": "task-bound-grant"},
		SecurityContext: &corev1.SecurityContext{
			Privileged:      k8sptr.To(false),
			RunAsNonRoot:    k8sptr.To(true),
			RunAsUser:       k8sptr.To(int64(1000)),
			SeccompProfile:  &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeUnconfined},
			AppArmorProfile: &corev1.AppArmorProfile{Type: corev1.AppArmorProfileTypeUnconfined},
		},
		Volumes:      []corev1.Volume{{Name: "buildkitd", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}},
		VolumeMounts: []corev1.VolumeMount{{Name: "buildkitd", MountPath: "/home/user/.local/share/buildkit"}},
	})

	pod := job.Spec.Template.Spec
	if pod.ServiceAccountName != "" || pod.AutomountServiceAccountToken == nil || *pod.AutomountServiceAccountToken {
		t.Fatalf("untrusted build pod received a service account capability: %#v", pod)
	}
	if len(pod.ImagePullSecrets) != 0 || len(pod.Containers[0].EnvFrom) != 0 {
		t.Fatalf("untrusted build pod received registry credentials: imagePullSecrets=%#v envFrom=%#v", pod.ImagePullSecrets, pod.Containers[0].EnvFrom)
	}
	container := pod.Containers[0]
	if container.SecurityContext == nil || container.SecurityContext.Privileged == nil || *container.SecurityContext.Privileged {
		t.Fatalf("build container must be non-privileged: %#v", container.SecurityContext)
	}
	if container.SecurityContext.RunAsNonRoot == nil || !*container.SecurityContext.RunAsNonRoot || container.SecurityContext.RunAsUser == nil || *container.SecurityContext.RunAsUser != 1000 {
		t.Fatalf("build container must run as rootless UID 1000: %#v", container.SecurityContext)
	}
	if container.SecurityContext.AllowPrivilegeEscalation != nil || container.SecurityContext.Capabilities != nil {
		t.Fatalf("rootless BuildKit must retain its default user-namespace boundary: %#v", container.SecurityContext)
	}
	if got := container.VolumeMounts; len(got) != 1 || got[0].Name != "buildkitd" || got[0].MountPath != "/home/user/.local/share/buildkit" {
		t.Fatalf("rootless BuildKit state mount = %#v", got)
	}
	if got := pod.Volumes; len(got) != 1 || got[0].EmptyDir == nil {
		t.Fatalf("rootless BuildKit state volume = %#v", got)
	}
	if got := container.Env; len(got) != 1 || got[0].Name != "VERIFY_UPLOAD_GRANT" {
		t.Fatalf("builder env = %#v", got)
	}
}

func TestNewJobSupportsSeparateTrustedPublisherCredentials(t *testing.T) {
	privileged := false
	automount := false
	job := newJob("breakfix-system", "publisher-vt-123", CreateJobOpts{
		Image:                        "public.example/breakfix-publisher:latest",
		ContainerName:                "publisher",
		Privileged:                   &privileged,
		AutomountServiceAccountToken: &automount,
		EnvSecretNames:               []string{"breakfix-registry-write"},
		ImagePullSecrets:             []corev1.LocalObjectReference{{Name: "breakfix-registry-pull"}},
	})
	pod := job.Spec.Template.Spec
	if pod.AutomountServiceAccountToken == nil || *pod.AutomountServiceAccountToken || pod.ServiceAccountName != "" {
		t.Fatalf("publisher must not receive Kubernetes API access: %#v", pod)
	}
	if got := pod.Containers[0].EnvFrom; len(got) != 1 || got[0].SecretRef == nil || got[0].SecretRef.Name != "breakfix-registry-write" {
		t.Fatalf("publisher write credential = %#v", got)
	}
	if got := pod.ImagePullSecrets; len(got) != 1 || got[0].Name != "breakfix-registry-pull" {
		t.Fatalf("publisher image pull secret = %#v", got)
	}
}
