package k8s

import (
	"context"
	"fmt"

	breakfixv1 "github.com/breakfix/breakfix/api/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
)

var (
	nodeEnvironmentGVR = schema.GroupVersionResource{Group: "breakfix.dev", Version: "v1", Resource: "nodeenvironments"}
	vk8sEnvironmentGVR = schema.GroupVersionResource{Group: "breakfix.dev", Version: "v1", Resource: "vk8senvironments"}
)

func (c *Client) crdClient() (dynamic.Interface, error) {
	return dynamic.NewForConfig(c.restConfig)
}

func (c *Client) CreateNodeEnvironment(ctx context.Context, ns string, env *breakfixv1.NodeEnvironment) (*breakfixv1.NodeEnvironment, error) {
	env.TypeMeta = metav1.TypeMeta{APIVersion: "breakfix.dev/v1", Kind: "NodeEnvironment"}
	return createCRD[*breakfixv1.NodeEnvironment](ctx, c, nodeEnvironmentGVR, ns, env, "create node environment")
}

func (c *Client) GetNodeEnvironment(ctx context.Context, ns, name string) (*breakfixv1.NodeEnvironment, error) {
	return getCRD[*breakfixv1.NodeEnvironment](ctx, c, nodeEnvironmentGVR, ns, name, "get node environment")
}

func (c *Client) UpdateNodeEnvironment(ctx context.Context, ns string, env *breakfixv1.NodeEnvironment) (*breakfixv1.NodeEnvironment, error) {
	env.TypeMeta = metav1.TypeMeta{APIVersion: "breakfix.dev/v1", Kind: "NodeEnvironment"}
	return updateCRD[*breakfixv1.NodeEnvironment](ctx, c, nodeEnvironmentGVR, ns, env, false, "update node environment")
}

func (c *Client) UpdateNodeEnvironmentStatus(ctx context.Context, ns string, env *breakfixv1.NodeEnvironment) (*breakfixv1.NodeEnvironment, error) {
	env.TypeMeta = metav1.TypeMeta{APIVersion: "breakfix.dev/v1", Kind: "NodeEnvironment"}
	return updateCRD[*breakfixv1.NodeEnvironment](ctx, c, nodeEnvironmentGVR, ns, env, true, "update node environment status")
}

func (c *Client) ListNodeEnvironments(ctx context.Context, ns, selector string) (*breakfixv1.NodeEnvironmentList, error) {
	return listCRD[breakfixv1.NodeEnvironment, breakfixv1.NodeEnvironmentList](ctx, c, nodeEnvironmentGVR, ns, selector, "list node environments")
}

func (c *Client) DeleteNodeEnvironment(ctx context.Context, ns, name string) error {
	return deleteCRD(ctx, c, nodeEnvironmentGVR, ns, name)
}

func (c *Client) DeleteNodeEnvironmentWithUID(ctx context.Context, ns, name string, uid types.UID) error {
	return deleteCRDWithUID(ctx, c, nodeEnvironmentGVR, ns, name, uid)
}

func (c *Client) CreateVK8sEnvironment(ctx context.Context, ns string, env *breakfixv1.VK8sEnvironment) (*breakfixv1.VK8sEnvironment, error) {
	env.TypeMeta = metav1.TypeMeta{APIVersion: "breakfix.dev/v1", Kind: "VK8sEnvironment"}
	return createCRD[*breakfixv1.VK8sEnvironment](ctx, c, vk8sEnvironmentGVR, ns, env, "create VK8s environment")
}

func (c *Client) GetVK8sEnvironment(ctx context.Context, ns, name string) (*breakfixv1.VK8sEnvironment, error) {
	return getCRD[*breakfixv1.VK8sEnvironment](ctx, c, vk8sEnvironmentGVR, ns, name, "get VK8s environment")
}

func (c *Client) UpdateVK8sEnvironment(ctx context.Context, ns string, env *breakfixv1.VK8sEnvironment) (*breakfixv1.VK8sEnvironment, error) {
	env.TypeMeta = metav1.TypeMeta{APIVersion: "breakfix.dev/v1", Kind: "VK8sEnvironment"}
	return updateCRD[*breakfixv1.VK8sEnvironment](ctx, c, vk8sEnvironmentGVR, ns, env, false, "update VK8s environment")
}

func (c *Client) UpdateVK8sEnvironmentStatus(ctx context.Context, ns string, env *breakfixv1.VK8sEnvironment) (*breakfixv1.VK8sEnvironment, error) {
	env.TypeMeta = metav1.TypeMeta{APIVersion: "breakfix.dev/v1", Kind: "VK8sEnvironment"}
	return updateCRD[*breakfixv1.VK8sEnvironment](ctx, c, vk8sEnvironmentGVR, ns, env, true, "update VK8s environment status")
}

func (c *Client) ListVK8sEnvironments(ctx context.Context, ns, selector string) (*breakfixv1.VK8sEnvironmentList, error) {
	return listCRD[breakfixv1.VK8sEnvironment, breakfixv1.VK8sEnvironmentList](ctx, c, vk8sEnvironmentGVR, ns, selector, "list VK8s environments")
}

func (c *Client) DeleteVK8sEnvironment(ctx context.Context, ns, name string) error {
	return deleteCRD(ctx, c, vk8sEnvironmentGVR, ns, name)
}

func (c *Client) DeleteVK8sEnvironmentWithUID(ctx context.Context, ns, name string, uid types.UID) error {
	return deleteCRDWithUID(ctx, c, vk8sEnvironmentGVR, ns, name, uid)
}

func createCRD[T any](ctx context.Context, c *Client, gvr schema.GroupVersionResource, ns string, obj T, action string) (T, error) {
	dyn, err := c.crdClient()
	if err != nil {
		var zero T
		return zero, err
	}
	u, err := toUnstructured(obj)
	if err != nil {
		var zero T
		return zero, err
	}
	result, err := dyn.Resource(gvr).Namespace(ns).Create(ctx, u, metav1.CreateOptions{})
	if err != nil {
		var zero T
		return zero, fmt.Errorf("%s: %w", action, err)
	}
	return fromUnstructured[T](result)
}

func getCRD[T any](ctx context.Context, c *Client, gvr schema.GroupVersionResource, ns, name, action string) (T, error) {
	dyn, err := c.crdClient()
	if err != nil {
		var zero T
		return zero, err
	}
	result, err := dyn.Resource(gvr).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		var zero T
		return zero, fmt.Errorf("%s %s: %w", action, name, err)
	}
	return fromUnstructured[T](result)
}

func updateCRD[T any](ctx context.Context, c *Client, gvr schema.GroupVersionResource, ns string, obj T, status bool, action string) (T, error) {
	dyn, err := c.crdClient()
	if err != nil {
		var zero T
		return zero, err
	}
	u, err := toUnstructured(obj)
	if err != nil {
		var zero T
		return zero, err
	}
	var result *unstructured.Unstructured
	if status {
		result, err = dyn.Resource(gvr).Namespace(ns).UpdateStatus(ctx, u, metav1.UpdateOptions{})
	} else {
		result, err = dyn.Resource(gvr).Namespace(ns).Update(ctx, u, metav1.UpdateOptions{})
	}
	if err != nil {
		var zero T
		return zero, fmt.Errorf("%s: %w", action, err)
	}
	return fromUnstructured[T](result)
}

func listCRD[T any, L any](ctx context.Context, c *Client, gvr schema.GroupVersionResource, ns, selector, action string) (*L, error) {
	dyn, err := c.crdClient()
	if err != nil {
		return nil, err
	}
	result, err := dyn.Resource(gvr).Namespace(ns).List(ctx, metav1.ListOptions{
		LabelSelector: selector,
	})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", action, err)
	}

	list := new(L)
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(result.Object, list); err != nil {
		return nil, fmt.Errorf("convert %s list metadata: %w", action, err)
	}

	var converted any = list
	switch typed := converted.(type) {
	case *breakfixv1.NodeEnvironmentList:
		typed.Items = make([]breakfixv1.NodeEnvironment, 0, len(result.Items))
		for _, item := range result.Items {
			var env breakfixv1.NodeEnvironment
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(item.Object, &env); err != nil {
				return nil, fmt.Errorf("convert node environment item: %w", err)
			}
			typed.Items = append(typed.Items, env)
		}
	case *breakfixv1.VK8sEnvironmentList:
		typed.Items = make([]breakfixv1.VK8sEnvironment, 0, len(result.Items))
		for _, item := range result.Items {
			var env breakfixv1.VK8sEnvironment
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(item.Object, &env); err != nil {
				return nil, fmt.Errorf("convert VK8s environment item: %w", err)
			}
			typed.Items = append(typed.Items, env)
		}
	default:
		return nil, fmt.Errorf("unsupported list type")
	}
	return list, nil
}

func deleteCRD(ctx context.Context, c *Client, gvr schema.GroupVersionResource, ns, name string) error {
	dyn, err := c.crdClient()
	if err != nil {
		return err
	}
	err = dyn.Resource(gvr).Namespace(ns).Delete(ctx, name, metav1.DeleteOptions{})
	if isNotFound(err) {
		return nil
	}
	return err
}

func deleteCRDWithUID(ctx context.Context, c *Client, gvr schema.GroupVersionResource, ns, name string, uid types.UID) error {
	if uid == "" {
		return fmt.Errorf("delete %s %q requires a UID precondition", gvr.Resource, name)
	}
	dyn, err := c.crdClient()
	if err != nil {
		return err
	}
	err = dyn.Resource(gvr).Namespace(ns).Delete(ctx, name, metav1.DeleteOptions{
		Preconditions: &metav1.Preconditions{UID: &uid},
	})
	if isNotFound(err) {
		return nil
	}
	return err
}
