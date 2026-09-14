package kubernetes

import (
	"context"

	runtimev2 "github.com/breakfix/breakfix/api/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

var runtimeEnvironmentGVR = schema.GroupVersionResource{Group: "breakfix.dev", Version: "v2", Resource: "runtimeenvironments"}

func (c *Client) CreateRuntimeEnvironment(ctx context.Context, namespace string, environment *runtimev2.RuntimeEnvironment) (*runtimev2.RuntimeEnvironment, error) {
	environment.TypeMeta = metav1.TypeMeta{APIVersion: "breakfix.dev/v2", Kind: "RuntimeEnvironment"}
	return createCRD[*runtimev2.RuntimeEnvironment](ctx, c, runtimeEnvironmentGVR, namespace, environment, "create runtime environment")
}

func (c *Client) GetRuntimeEnvironment(ctx context.Context, namespace, name string) (*runtimev2.RuntimeEnvironment, error) {
	return getCRD[*runtimev2.RuntimeEnvironment](ctx, c, runtimeEnvironmentGVR, namespace, name, "get runtime environment")
}

func (c *Client) UpdateRuntimeEnvironment(ctx context.Context, namespace string, environment *runtimev2.RuntimeEnvironment) (*runtimev2.RuntimeEnvironment, error) {
	environment.TypeMeta = metav1.TypeMeta{APIVersion: "breakfix.dev/v2", Kind: "RuntimeEnvironment"}
	return updateCRD[*runtimev2.RuntimeEnvironment](ctx, c, runtimeEnvironmentGVR, namespace, environment, false, "update runtime environment")
}

func (c *Client) UpdateRuntimeEnvironmentStatus(ctx context.Context, namespace string, environment *runtimev2.RuntimeEnvironment) (*runtimev2.RuntimeEnvironment, error) {
	environment.TypeMeta = metav1.TypeMeta{APIVersion: "breakfix.dev/v2", Kind: "RuntimeEnvironment"}
	return updateCRD[*runtimev2.RuntimeEnvironment](ctx, c, runtimeEnvironmentGVR, namespace, environment, true, "update runtime environment status")
}

func (c *Client) ListRuntimeEnvironments(ctx context.Context, namespace, selector string) (*runtimev2.RuntimeEnvironmentList, error) {
	return listCRD[runtimev2.RuntimeEnvironment, runtimev2.RuntimeEnvironmentList](ctx, c, runtimeEnvironmentGVR, namespace, selector, "list runtime environments")
}

func (c *Client) DeleteRuntimeEnvironment(ctx context.Context, namespace, name string) error {
	return deleteCRD(ctx, c, runtimeEnvironmentGVR, namespace, name)
}

func (c *Client) DeleteRuntimeEnvironmentWithUID(ctx context.Context, namespace, name string, uid types.UID) error {
	return deleteCRDWithUID(ctx, c, runtimeEnvironmentGVR, namespace, name, uid)
}
