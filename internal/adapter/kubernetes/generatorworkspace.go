package kubernetes

import (
	"context"
	"errors"
	"fmt"
	"strings"

	apiv2 "github.com/breakfix/breakfix/api/v2"
	appgeneration "github.com/breakfix/breakfix/internal/application/generation"
	domain "github.com/breakfix/breakfix/internal/domain/generation"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// GeneratorWorkspaceOwner keeps one GeneratorWorkspace CR per Generator
// workspace. The CR is the cluster-side owner: PVCs reference it through
// ownerReferences and the cleanup finalizer is removed only after the
// cluster-external Sandbox is gone.
type GeneratorWorkspaceOwner struct {
	client    client.Client
	namespace string
}

// NewGeneratorWorkspaceOwner builds the CR client from the shared REST
// config. Only the Breakfix API scheme is installed: this client never
// touches core resources.
func NewGeneratorWorkspaceOwner(restConfig *rest.Config, namespace string) (*GeneratorWorkspaceOwner, error) {
	if restConfig == nil || strings.TrimSpace(namespace) == "" {
		return nil, errors.New("generator workspace owner requires a rest config and namespace")
	}
	scheme := runtime.NewScheme()
	if err := apiv2.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("add generator workspace scheme: %w", err)
	}
	crClient, err := client.New(restConfig, client.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("create generator workspace client: %w", err)
	}
	return &GeneratorWorkspaceOwner{client: crClient, namespace: strings.TrimSpace(namespace)}, nil
}

func (o *GeneratorWorkspaceOwner) EnsureWorkspaceOwner(ctx context.Context, record domain.Workspace) (appgeneration.WorkspaceOwnerReference, error) {
	if o == nil || o.client == nil || strings.TrimSpace(record.ID) == "" {
		return appgeneration.WorkspaceOwnerReference{}, errors.New("generator workspace owner client and workspace id are required")
	}
	current := &apiv2.GeneratorWorkspace{}
	err := o.client.Get(ctx, client.ObjectKey{Namespace: o.namespace, Name: record.ID}, current)
	if apierrors.IsNotFound(err) {
		created := o.newObject(record)
		if err := o.client.Create(ctx, created); err != nil && !apierrors.IsAlreadyExists(err) {
			return appgeneration.WorkspaceOwnerReference{}, fmt.Errorf("create generator workspace owner %s: %w", record.ID, err)
		}
		if apierrors.IsAlreadyExists(err) {
			if err := o.client.Get(ctx, client.ObjectKey{Namespace: o.namespace, Name: record.ID}, current); err != nil {
				return appgeneration.WorkspaceOwnerReference{}, fmt.Errorf("get generator workspace owner %s: %w", record.ID, err)
			}
		} else {
			current = created
		}
	} else if err != nil {
		return appgeneration.WorkspaceOwnerReference{}, fmt.Errorf("get generator workspace owner %s: %w", record.ID, err)
	}
	if err := o.ensureFinalizer(ctx, current); err != nil {
		return appgeneration.WorkspaceOwnerReference{}, err
	}
	return ownerReference(current), nil
}

func (o *GeneratorWorkspaceOwner) RecordWorkspaceOwnerStatus(ctx context.Context, record domain.Workspace) error {
	if o == nil || o.client == nil || strings.TrimSpace(record.ID) == "" {
		return errors.New("generator workspace owner client and workspace id are required")
	}
	current := &apiv2.GeneratorWorkspace{}
	err := o.client.Get(ctx, client.ObjectKey{Namespace: o.namespace, Name: record.ID}, current)
	if apierrors.IsNotFound(err) {
		// The CR is the durable ownership record; a lost CR is recreated from
		// the row rather than left for the garbage collector to forget.
		return o.client.Create(ctx, o.newObject(record))
	}
	if err != nil {
		return fmt.Errorf("get generator workspace owner %s: %w", record.ID, err)
	}
	if ownerStatus(record) == current.Status {
		return nil
	}
	patch := client.MergeFrom(current.DeepCopy())
	current.Status = ownerStatus(record)
	if err := o.client.Status().Patch(ctx, current, patch); err != nil {
		return fmt.Errorf("patch generator workspace owner status %s: %w", record.ID, err)
	}
	return nil
}

func (o *GeneratorWorkspaceOwner) RetireWorkspaceOwner(ctx context.Context, workspaceID string) error {
	if o == nil || o.client == nil || strings.TrimSpace(workspaceID) == "" {
		return errors.New("generator workspace owner client and workspace id are required")
	}
	current := &apiv2.GeneratorWorkspace{}
	err := o.client.Get(ctx, client.ObjectKey{Namespace: o.namespace, Name: strings.TrimSpace(workspaceID)}, current)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get generator workspace owner %s: %w", workspaceID, err)
	}
	// A CR already being deleted needs no phase transition; the finalizer flow
	// owns its remaining lifecycle.
	if current.Status.Phase == apiv2.WorkspacePhaseDeleting || !current.DeletionTimestamp.IsZero() {
		return nil
	}
	patch := client.MergeFrom(current.DeepCopy())
	current.Status.Phase = apiv2.WorkspacePhaseDeleting
	if err := o.client.Status().Patch(ctx, current, patch); err != nil {
		return fmt.Errorf("retire generator workspace owner %s: %w", workspaceID, err)
	}
	return nil
}

// DeleteWorkspaceOwner removes the cleanup finalizer and deletes the CR. The
// caller must already have dropped the cluster-external Sandbox: removing the
// finalizer is the statement that the external delete succeeded, and the API
// server then lets the garbage collector cascade to the owned PVC.
func (o *GeneratorWorkspaceOwner) DeleteWorkspaceOwner(ctx context.Context, workspaceID string) error {
	if o == nil || o.client == nil || strings.TrimSpace(workspaceID) == "" {
		return errors.New("generator workspace owner client and workspace id are required")
	}
	current := &apiv2.GeneratorWorkspace{}
	err := o.client.Get(ctx, client.ObjectKey{Namespace: o.namespace, Name: strings.TrimSpace(workspaceID)}, current)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get generator workspace owner %s: %w", workspaceID, err)
	}
	if hasGeneratorWorkspaceFinalizer(current) {
		patch := client.MergeFrom(current.DeepCopy())
		finalizers := make([]string, 0, len(current.Finalizers))
		for _, finalizer := range current.Finalizers {
			if finalizer != apiv2.GeneratorWorkspaceCleanupFinalizer {
				finalizers = append(finalizers, finalizer)
			}
		}
		current.Finalizers = finalizers
		if err := o.client.Patch(ctx, current, patch); err != nil {
			return fmt.Errorf("remove generator workspace owner finalizer %s: %w", workspaceID, err)
		}
	}
	if err := o.client.Delete(ctx, current); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete generator workspace owner %s: %w", workspaceID, err)
	}
	return nil
}

func (o *GeneratorWorkspaceOwner) ListWorkspaceOwners(ctx context.Context) ([]domain.WorkspaceOwner, error) {
	if o == nil || o.client == nil {
		return nil, errors.New("generator workspace owner client is required")
	}
	list := &apiv2.GeneratorWorkspaceList{}
	if err := o.client.List(ctx, list, client.InNamespace(o.namespace)); err != nil {
		return nil, fmt.Errorf("list generator workspace owners: %w", err)
	}
	owners := make([]domain.WorkspaceOwner, 0, len(list.Items))
	for _, item := range list.Items {
		state, err := domainState(item.Status.Phase)
		if err != nil {
			return nil, fmt.Errorf("generator workspace owner %s: %w", item.Name, err)
		}
		owners = append(owners, domain.WorkspaceOwner{
			ID:         item.Name,
			WorkflowID: item.Status.WorkflowID,
			Namespace:  item.Status.Namespace,
			PVCName:    item.Status.PVCName,
			SandboxID:  item.Status.SandboxID,
			State:      state,
		})
	}
	return owners, nil
}

func (o *GeneratorWorkspaceOwner) newObject(record domain.Workspace) *apiv2.GeneratorWorkspace {
	return &apiv2.GeneratorWorkspace{
		ObjectMeta: metav1.ObjectMeta{
			Name:       record.ID,
			Namespace:  o.namespace,
			Finalizers: []string{apiv2.GeneratorWorkspaceCleanupFinalizer},
		},
		Spec:   apiv2.GeneratorWorkspaceSpec{},
		Status: ownerStatus(record),
	}
}

func (o *GeneratorWorkspaceOwner) ensureFinalizer(ctx context.Context, current *apiv2.GeneratorWorkspace) error {
	if hasGeneratorWorkspaceFinalizer(current) {
		return nil
	}
	patch := client.MergeFrom(current.DeepCopy())
	current.Finalizers = append(current.Finalizers, apiv2.GeneratorWorkspaceCleanupFinalizer)
	if err := o.client.Patch(ctx, current, patch); err != nil {
		return fmt.Errorf("add generator workspace owner finalizer %s: %w", current.Name, err)
	}
	return nil
}

func hasGeneratorWorkspaceFinalizer(object *apiv2.GeneratorWorkspace) bool {
	for _, finalizer := range object.Finalizers {
		if finalizer == apiv2.GeneratorWorkspaceCleanupFinalizer {
			return true
		}
	}
	return false
}

func ownerStatus(record domain.Workspace) apiv2.GeneratorWorkspaceStatus {
	return apiv2.GeneratorWorkspaceStatus{
		WorkflowID: record.WorkflowID,
		Namespace:  record.Namespace,
		PVCName:    record.PVCName,
		SandboxID:  strings.TrimSpace(record.SandboxID),
		Phase:      workspacePhase(record.State),
	}
}

func workspacePhase(state domain.WorkspaceState) apiv2.WorkspacePhase {
	switch state {
	case domain.WorkspaceActive:
		return apiv2.WorkspacePhaseActive
	case domain.WorkspaceDeleting:
		return apiv2.WorkspacePhaseDeleting
	default:
		return apiv2.WorkspacePhasePending
	}
}

func domainState(phase apiv2.WorkspacePhase) (domain.WorkspaceState, error) {
	switch phase {
	case apiv2.WorkspacePhasePending:
		return domain.WorkspacePending, nil
	case apiv2.WorkspacePhaseActive:
		return domain.WorkspaceActive, nil
	case apiv2.WorkspacePhaseDeleting:
		return domain.WorkspaceDeleting, nil
	default:
		return "", fmt.Errorf("invalid workspace phase %q", phase)
	}
}

func ownerReference(object *apiv2.GeneratorWorkspace) appgeneration.WorkspaceOwnerReference {
	return appgeneration.WorkspaceOwnerReference{
		APIVersion: apiv2.SchemeGroupVersion.String(),
		Kind:       "GeneratorWorkspace",
		Name:       object.Name,
		UID:        string(object.UID),
	}
}
