package kubernetes

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	k8sErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EnsureImagePullSecret copies a Docker config Secret from the control-plane
// namespace into a dynamically created environment namespace.
func (c *Client) EnsureImagePullSecret(ctx context.Context, sourceNamespace, targetNamespace, name string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	sourceNamespace = strings.TrimSpace(sourceNamespace)
	targetNamespace = strings.TrimSpace(targetNamespace)
	name = strings.TrimSpace(name)
	if sourceNamespace == "" || targetNamespace == "" || name == "" {
		return fmt.Errorf("registry pull secret source namespace, target namespace, and name are required")
	}
	source, err := c.clientset.CoreV1().Secrets(sourceNamespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get registry pull secret: %w", err)
	}
	if source.Type != corev1.SecretTypeDockerConfigJson || len(source.Data[corev1.DockerConfigJsonKey]) == 0 {
		return fmt.Errorf("registry pull secret %s/%s must be a docker config secret", sourceNamespace, name)
	}
	if sourceNamespace == targetNamespace {
		return nil
	}
	targets := c.clientset.CoreV1().Secrets(targetNamespace)
	current, err := targets.Get(ctx, name, metav1.GetOptions{})
	if k8sErrors.IsNotFound(err) {
		_, err = targets.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: targetNamespace,
				Labels: map[string]string{
					"app.kubernetes.io/part-of":  "breakfix",
					"breakfix.dev/registry-pull": "true",
				},
			},
			Type: corev1.SecretTypeDockerConfigJson,
			Data: map[string][]byte{
				corev1.DockerConfigJsonKey: append([]byte(nil), source.Data[corev1.DockerConfigJsonKey]...),
			},
		}, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return fmt.Errorf("get target registry pull secret: %w", err)
	}
	if current.Type != corev1.SecretTypeDockerConfigJson {
		return fmt.Errorf("target registry pull secret %s/%s is not a docker config secret", targetNamespace, name)
	}
	current.Data = map[string][]byte{corev1.DockerConfigJsonKey: append([]byte(nil), source.Data[corev1.DockerConfigJsonKey]...)}
	_, err = targets.Update(ctx, current, metav1.UpdateOptions{})
	return err
}

// EnsureRuntimeServiceAccount creates the environment runtime ServiceAccount
// without an API token. A Registry pull Secret is optional because public OCI
// Registries do not require one.
func (c *Client) EnsureRuntimeServiceAccount(ctx context.Context, namespace, serviceAccountName, secretName string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	namespace = strings.TrimSpace(namespace)
	serviceAccountName = strings.TrimSpace(serviceAccountName)
	secretName = strings.TrimSpace(secretName)
	if namespace == "" || serviceAccountName == "" {
		return fmt.Errorf("service account namespace and name are required")
	}

	serviceAccounts := c.clientset.CoreV1().ServiceAccounts(namespace)
	var desiredSecrets []corev1.LocalObjectReference
	if secretName != "" {
		desiredSecrets = []corev1.LocalObjectReference{{Name: secretName}}
	}
	disableTokenMount := false
	serviceAccount, err := serviceAccounts.Get(ctx, serviceAccountName, metav1.GetOptions{})
	if k8sErrors.IsNotFound(err) {
		_, err = serviceAccounts.Create(ctx, &corev1.ServiceAccount{
			ObjectMeta:                   metav1.ObjectMeta{Name: serviceAccountName, Namespace: namespace},
			AutomountServiceAccountToken: &disableTokenMount,
			ImagePullSecrets:             desiredSecrets,
		}, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return fmt.Errorf("get runtime service account: %w", err)
	}
	if serviceAccount.AutomountServiceAccountToken != nil && !*serviceAccount.AutomountServiceAccountToken && reflect.DeepEqual(serviceAccount.ImagePullSecrets, desiredSecrets) {
		return nil
	}
	serviceAccount.AutomountServiceAccountToken = &disableTokenMount
	serviceAccount.ImagePullSecrets = desiredSecrets
	_, err = serviceAccounts.Update(ctx, serviceAccount, metav1.UpdateOptions{})
	return err
}

func (c *Client) UpsertSecret(namespace, name string, data map[string][]byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	secrets := c.clientset.CoreV1().Secrets(namespace)
	current, err := secrets.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if !k8sErrors.IsNotFound(err) {
			return err
		}
		_, err = secrets.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Type:       corev1.SecretTypeOpaque,
			Data:       data,
		}, metav1.CreateOptions{})
		return err
	}

	current.Data = data
	_, err = secrets.Update(ctx, current, metav1.UpdateOptions{})
	return err
}

func (c *Client) DeleteSecret(namespace, name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.clientset.CoreV1().Secrets(namespace).Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !k8sErrors.IsNotFound(err) {
		return err
	}
	return nil
}
