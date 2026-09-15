package kubernetes

import (
	"context"

	runtimeenvironment "github.com/breakfix/breakfix/internal/controller/runtimeenvironment"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
)

// LegacyEnvironmentDrainClient adapts the v1 CRD client to the narrow drain
// port. It intentionally exposes no v1 create/update operations.
type LegacyEnvironmentDrainClient struct{ Client *Client }

func (c LegacyEnvironmentDrainClient) ListNode(ctx context.Context, namespace string) ([]runtimeenvironment.LegacyResource, error) {
	items, err := c.Client.ListNodeEnvironments(ctx, namespace, "")
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	result := make([]runtimeenvironment.LegacyResource, 0, len(items.Items))
	for _, item := range items.Items {
		result = append(result, runtimeenvironment.LegacyResource{Name: item.Name, UID: string(item.UID)})
	}
	return result, nil
}

func (c LegacyEnvironmentDrainClient) ListK8s(ctx context.Context, namespace string) ([]runtimeenvironment.LegacyResource, error) {
	items, err := c.Client.ListVK8sEnvironments(ctx, namespace, "")
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	result := make([]runtimeenvironment.LegacyResource, 0, len(items.Items))
	for _, item := range items.Items {
		result = append(result, runtimeenvironment.LegacyResource{Name: item.Name, UID: string(item.UID)})
	}
	return result, nil
}

func (c LegacyEnvironmentDrainClient) DeleteNode(ctx context.Context, namespace string, resource runtimeenvironment.LegacyResource) error {
	err := c.Client.DeleteNodeEnvironmentWithUID(ctx, namespace, resource.Name, uid(resource.UID))
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

func (c LegacyEnvironmentDrainClient) DeleteK8s(ctx context.Context, namespace string, resource runtimeenvironment.LegacyResource) error {
	err := c.Client.DeleteVK8sEnvironmentWithUID(ctx, namespace, resource.Name, uid(resource.UID))
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

func uid(value string) types.UID { return types.UID(value) }
