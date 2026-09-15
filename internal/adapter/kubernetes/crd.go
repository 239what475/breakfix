package kubernetes

import (
	"context"
	"fmt"

	runtimev2 "github.com/breakfix/breakfix/api/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
)

func (c *Client) crdClient() (dynamic.Interface, error) {
	return dynamic.NewForConfig(c.restConfig)
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
	case *runtimev2.RuntimeEnvironmentList:
		typed.Items = make([]runtimev2.RuntimeEnvironment, 0, len(result.Items))
		for _, item := range result.Items {
			var env runtimev2.RuntimeEnvironment
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(item.Object, &env); err != nil {
				return nil, fmt.Errorf("convert runtime environment item: %w", err)
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
