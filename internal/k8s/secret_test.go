package k8s

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestEnsureImagePullSecretCopiesOnlyDockerConfig(t *testing.T) {
	client := &Client{clientset: fake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "breakfix-registry-pull", Namespace: "breakfix-system"},
		Type:       corev1.SecretTypeDockerConfigJson,
		Data:       map[string][]byte{corev1.DockerConfigJsonKey: []byte(`{"auths":{"registry.example":{}}}`)},
	})}
	if err := client.EnsureImagePullSecret(context.Background(), "breakfix-system", "breakfix-user-one", "breakfix-registry-pull"); err != nil {
		t.Fatal(err)
	}
	secret, err := client.clientset.CoreV1().Secrets("breakfix-user-one").Get(context.Background(), "breakfix-registry-pull", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if secret.Type != corev1.SecretTypeDockerConfigJson || string(secret.Data[corev1.DockerConfigJsonKey]) != `{"auths":{"registry.example":{}}}` {
		t.Fatalf("copied pull secret = %#v", secret)
	}
}

func TestEnsureRuntimeServiceAccountBindsOnlyPullCredential(t *testing.T) {
	client := &Client{clientset: fake.NewSimpleClientset(&corev1.ServiceAccount{
		ObjectMeta:       metav1.ObjectMeta{Name: "breakfix-runtime", Namespace: "breakfix-environment"},
		ImagePullSecrets: []corev1.LocalObjectReference{{Name: "stale-secret"}},
	})}
	if err := client.EnsureRuntimeServiceAccount(context.Background(), "breakfix-environment", "breakfix-runtime", "breakfix-registry-pull"); err != nil {
		t.Fatal(err)
	}

	serviceAccount, err := client.clientset.CoreV1().ServiceAccounts("breakfix-environment").Get(context.Background(), "breakfix-runtime", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if serviceAccount.AutomountServiceAccountToken == nil || *serviceAccount.AutomountServiceAccountToken {
		t.Fatalf("automount service account token = %#v, want false", serviceAccount.AutomountServiceAccountToken)
	}
	if got := serviceAccount.ImagePullSecrets; len(got) != 1 || got[0].Name != "breakfix-registry-pull" {
		t.Fatalf("image pull secrets = %#v", got)
	}
}

func TestEnsureRuntimeServiceAccountSupportsAnonymousRegistry(t *testing.T) {
	client := &Client{clientset: fake.NewSimpleClientset(&corev1.ServiceAccount{
		ObjectMeta:       metav1.ObjectMeta{Name: "breakfix-runtime", Namespace: "breakfix-environment"},
		ImagePullSecrets: []corev1.LocalObjectReference{{Name: "stale-secret"}},
	})}
	if err := client.EnsureRuntimeServiceAccount(context.Background(), "breakfix-environment", "breakfix-runtime", ""); err != nil {
		t.Fatal(err)
	}

	serviceAccount, err := client.clientset.CoreV1().ServiceAccounts("breakfix-environment").Get(context.Background(), "breakfix-runtime", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if serviceAccount.AutomountServiceAccountToken == nil || *serviceAccount.AutomountServiceAccountToken {
		t.Fatalf("automount service account token = %#v, want false", serviceAccount.AutomountServiceAccountToken)
	}
	if len(serviceAccount.ImagePullSecrets) != 0 {
		t.Fatalf("image pull secrets = %#v, want none", serviceAccount.ImagePullSecrets)
	}
}
