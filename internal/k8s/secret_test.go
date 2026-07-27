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
	if err := client.EnsureImagePullSecret("breakfix-system", "breakfix-user-one", "breakfix-registry-pull"); err != nil {
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
