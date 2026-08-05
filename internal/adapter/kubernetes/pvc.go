package kubernetes

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	k8sErrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EnsureWorkspacePVC creates the Server-owned BYO claim OpenSandbox mounts.
// A pre-existing claim is accepted only when it belongs to the same workflow.
func (c *Client) EnsureWorkspacePVC(ctx context.Context, namespace, name, workflowID, storage string) error {
	namespace = strings.TrimSpace(namespace)
	name = strings.TrimSpace(name)
	workflowID = strings.TrimSpace(workflowID)
	quantity, err := resource.ParseQuantity(strings.TrimSpace(storage))
	if namespace == "" || name == "" || workflowID == "" || err != nil || quantity.Sign() <= 0 {
		return fmt.Errorf("workspace pvc requires namespace, name, workflow id, and positive storage")
	}
	claims := c.clientset.CoreV1().PersistentVolumeClaims(namespace)
	current, err := claims.Get(ctx, name, metav1.GetOptions{})
	if k8sErrors.IsNotFound(err) {
		_, err = claims.Create(ctx, &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
				Labels: map[string]string{
					"app.kubernetes.io/part-of": "breakfix",
					"breakfix.dev/workflow":     workflowID,
				},
			},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources:   corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: quantity}},
			},
		}, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return fmt.Errorf("get workspace pvc: %w", err)
	}
	if current.Labels["breakfix.dev/workflow"] != workflowID {
		return fmt.Errorf("workspace pvc %s is not owned by generation workflow", name)
	}
	return nil
}

func (c *Client) DeleteWorkspacePVC(ctx context.Context, namespace, name string) error {
	deleteCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	claims := c.clientset.CoreV1().PersistentVolumeClaims(strings.TrimSpace(namespace))
	err := claims.Delete(deleteCtx, strings.TrimSpace(name), metav1.DeleteOptions{})
	if err != nil && !k8sErrors.IsNotFound(err) {
		return fmt.Errorf("delete workspace pvc: %w", err)
	}
	for {
		_, err := claims.Get(deleteCtx, strings.TrimSpace(name), metav1.GetOptions{})
		if k8sErrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("wait for workspace pvc deletion: %w", err)
		}
		select {
		case <-deleteCtx.Done():
			return fmt.Errorf("wait for workspace pvc deletion: %w", deleteCtx.Err())
		case <-time.After(250 * time.Millisecond):
		}
	}
}
