package kubernetes

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	appgeneration "github.com/breakfix/breakfix/internal/application/generation"
	corev1 "k8s.io/api/core/v1"
	k8sErrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
)

// EnsureWorkspacePVC creates the Server-owned BYO claim OpenSandbox mounts,
// owned by the workspace's GeneratorWorkspace CR so Kubernetes garbage
// collection drops it with the CR. A pre-existing claim is accepted only when
// it belongs to the same workflow, and gains the owner reference if it was
// provisioned before the ownership transfer.
func (c *Client) EnsureWorkspacePVC(ctx context.Context, namespace, name, workflowID, storage string, owner appgeneration.WorkspaceOwnerReference) error {
	namespace = strings.TrimSpace(namespace)
	name = strings.TrimSpace(name)
	workflowID = strings.TrimSpace(workflowID)
	quantity, err := resource.ParseQuantity(strings.TrimSpace(storage))
	if namespace == "" || name == "" || workflowID == "" || err != nil || quantity.Sign() <= 0 {
		return fmt.Errorf("workspace pvc requires namespace, name, workflow id, and positive storage")
	}
	if !ownerValid(owner) {
		return fmt.Errorf("workspace pvc %s requires a generator workspace owner reference", name)
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
				OwnerReferences: []metav1.OwnerReference{ownerReferenceValue(owner)},
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
	return ensurePVCOwnerReference(ctx, claims, current, owner)
}

// EnsureWorkspacePVCOwner guarantees an existing claim references its
// GeneratorWorkspace owner without ever creating the claim: the drop path
// calls it so the garbage collection cascade reaches claims provisioned
// before the ownership transfer.
func (c *Client) EnsureWorkspacePVCOwner(ctx context.Context, namespace, name string, owner appgeneration.WorkspaceOwnerReference) error {
	namespace = strings.TrimSpace(namespace)
	name = strings.TrimSpace(name)
	if namespace == "" || name == "" || !ownerValid(owner) {
		return fmt.Errorf("workspace pvc owner requires namespace, name, and a generator workspace owner reference")
	}
	claims := c.clientset.CoreV1().PersistentVolumeClaims(namespace)
	current, err := claims.Get(ctx, name, metav1.GetOptions{})
	if k8sErrors.IsNotFound(err) {
		// Nothing to own: the drop never creates resources.
		return nil
	}
	if err != nil {
		return fmt.Errorf("get workspace pvc: %w", err)
	}
	return ensurePVCOwnerReference(ctx, claims, current, owner)
}

// ensurePVCOwnerReference attaches the GeneratorWorkspace owner to a claim
// provisioned before the ownership transfer, so a database reset can still
// reclaim it through the CR.
func ensurePVCOwnerReference(ctx context.Context, claims typedcorev1.PersistentVolumeClaimInterface, current *corev1.PersistentVolumeClaim, owner appgeneration.WorkspaceOwnerReference) error {
	references, changed := upsertOwnerReference(current.OwnerReferences, owner)
	if !changed {
		return nil
	}
	patch, err := json.Marshal(map[string]any{"metadata": map[string]any{"ownerReferences": references}})
	if err != nil {
		return fmt.Errorf("encode workspace pvc owner patch: %w", err)
	}
	if _, err := claims.Patch(ctx, current.Name, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
		return fmt.Errorf("patch workspace pvc owner: %w", err)
	}
	return nil
}

func ownerValid(owner appgeneration.WorkspaceOwnerReference) bool {
	return owner.APIVersion != "" && owner.Kind != "" && owner.Name != "" && owner.UID != ""
}

func ownerReferenceValue(owner appgeneration.WorkspaceOwnerReference) metav1.OwnerReference {
	return metav1.OwnerReference{APIVersion: owner.APIVersion, Kind: owner.Kind, Name: owner.Name, UID: types.UID(owner.UID)}
}

func upsertOwnerReference(references []metav1.OwnerReference, owner appgeneration.WorkspaceOwnerReference) ([]metav1.OwnerReference, bool) {
	for _, reference := range references {
		if string(reference.UID) == owner.UID {
			return references, false
		}
	}
	return append(references, ownerReferenceValue(owner)), true
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
