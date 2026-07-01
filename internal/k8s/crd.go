package k8s

import (
	"context"
	"fmt"

	breakfixv1 "github.com/breakfix/breakfix/apis/breakfix/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
)

var (
	generationGVR = schema.GroupVersionResource{Group: "breakfix.dev", Version: "v1", Resource: "generations"}
	instanceGVR   = schema.GroupVersionResource{Group: "breakfix.dev", Version: "v1", Resource: "instances"}
)

// crdClient lazily creates a dynamic client for CRD operations.
func (c *Client) crdClient() (dynamic.Interface, error) {
	return dynamic.NewForConfig(c.restConfig)
}

// ── Generation CRD ──

func (c *Client) CreateGeneration(ctx context.Context, ns string, gen *breakfixv1.Generation) (*breakfixv1.Generation, error) {
	gen.TypeMeta = metav1.TypeMeta{APIVersion: "breakfix.dev/v1", Kind: "Generation"}
	dyn, err := c.crdClient()
	if err != nil {
		return nil, err
	}
	obj, err := toUnstructured(gen)
	if err != nil {
		return nil, err
	}
	result, err := dyn.Resource(generationGVR).Namespace(ns).Create(ctx, obj, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("create generation: %w", err)
	}
	return fromUnstructured[*breakfixv1.Generation](result)
}

func (c *Client) GetGeneration(ctx context.Context, ns, name string) (*breakfixv1.Generation, error) {
	dyn, err := c.crdClient()
	if err != nil {
		return nil, err
	}
	result, err := dyn.Resource(generationGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get generation %s: %w", name, err)
	}
	return fromUnstructured[*breakfixv1.Generation](result)
}

func (c *Client) UpdateGeneration(ctx context.Context, ns string, gen *breakfixv1.Generation) (*breakfixv1.Generation, error) {
	gen.TypeMeta = metav1.TypeMeta{APIVersion: "breakfix.dev/v1", Kind: "Generation"}
	dyn, err := c.crdClient()
	if err != nil {
		return nil, err
	}
	obj, err := toUnstructured(gen)
	if err != nil {
		return nil, err
	}
	result, err := dyn.Resource(generationGVR).Namespace(ns).Update(ctx, obj, metav1.UpdateOptions{})
	if err != nil {
		return nil, fmt.Errorf("update generation: %w", err)
	}
	return fromUnstructured[*breakfixv1.Generation](result)
}

func (c *Client) UpdateGenerationStatus(ctx context.Context, ns string, gen *breakfixv1.Generation) (*breakfixv1.Generation, error) {
	gen.TypeMeta = metav1.TypeMeta{APIVersion: "breakfix.dev/v1", Kind: "Generation"}
	dyn, err := c.crdClient()
	if err != nil {
		return nil, err
	}
	obj, err := toUnstructured(gen)
	if err != nil {
		return nil, err
	}
	result, err := dyn.Resource(generationGVR).Namespace(ns).UpdateStatus(ctx, obj, metav1.UpdateOptions{})
	if err != nil {
		return nil, fmt.Errorf("update generation status: %w", err)
	}
	return fromUnstructured[*breakfixv1.Generation](result)
}

func (c *Client) ListGenerations(ctx context.Context, ns string) (*breakfixv1.GenerationList, error) {
	dyn, err := c.crdClient()
	if err != nil {
		return nil, err
	}
	result, err := dyn.Resource(generationGVR).Namespace(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list generations: %w", err)
	}

	var list breakfixv1.GenerationList
	list.Items = make([]breakfixv1.Generation, 0, len(result.Items))
	for _, item := range result.Items {
		var gen breakfixv1.Generation
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(item.Object, &gen); err != nil {
			return nil, fmt.Errorf("convert generation item: %w", err)
		}
		list.Items = append(list.Items, gen)
	}
	return &list, nil
}

// ── Instance CRD ──

func (c *Client) CreateInstance(ctx context.Context, ns string, inst *breakfixv1.Instance) (*breakfixv1.Instance, error) {
	inst.TypeMeta = metav1.TypeMeta{APIVersion: "breakfix.dev/v1", Kind: "Instance"}
	dyn, err := c.crdClient()
	if err != nil {
		return nil, err
	}
	obj, err := toUnstructured(inst)
	if err != nil {
		return nil, err
	}
	result, err := dyn.Resource(instanceGVR).Namespace(ns).Create(ctx, obj, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("create instance: %w", err)
	}
	return fromUnstructured[*breakfixv1.Instance](result)
}

func (c *Client) GetInstance(ctx context.Context, ns, name string) (*breakfixv1.Instance, error) {
	dyn, err := c.crdClient()
	if err != nil {
		return nil, err
	}
	result, err := dyn.Resource(instanceGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get instance %s: %w", name, err)
	}
	return fromUnstructured[*breakfixv1.Instance](result)
}

func (c *Client) UpdateInstance(ctx context.Context, ns string, inst *breakfixv1.Instance) (*breakfixv1.Instance, error) {
	inst.TypeMeta = metav1.TypeMeta{APIVersion: "breakfix.dev/v1", Kind: "Instance"}
	dyn, err := c.crdClient()
	if err != nil {
		return nil, err
	}
	obj, err := toUnstructured(inst)
	if err != nil {
		return nil, err
	}
	result, err := dyn.Resource(instanceGVR).Namespace(ns).Update(ctx, obj, metav1.UpdateOptions{})
	if err != nil {
		return nil, fmt.Errorf("update instance: %w", err)
	}
	return fromUnstructured[*breakfixv1.Instance](result)
}

func (c *Client) UpdateInstanceStatus(ctx context.Context, ns string, inst *breakfixv1.Instance) (*breakfixv1.Instance, error) {
	inst.TypeMeta = metav1.TypeMeta{APIVersion: "breakfix.dev/v1", Kind: "Instance"}
	dyn, err := c.crdClient()
	if err != nil {
		return nil, err
	}
	obj, err := toUnstructured(inst)
	if err != nil {
		return nil, err
	}
	result, err := dyn.Resource(instanceGVR).Namespace(ns).UpdateStatus(ctx, obj, metav1.UpdateOptions{})
	if err != nil {
		return nil, fmt.Errorf("update instance status: %w", err)
	}
	return fromUnstructured[*breakfixv1.Instance](result)
}

func (c *Client) ListInstances(ctx context.Context, ns string, selector string) (*breakfixv1.InstanceList, error) {
	dyn, err := c.crdClient()
	if err != nil {
		return nil, err
	}
	result, err := dyn.Resource(instanceGVR).Namespace(ns).List(ctx, metav1.ListOptions{
		LabelSelector: selector,
	})
	if err != nil {
		return nil, fmt.Errorf("list instances: %w", err)
	}

	// Convert items one by one from unstructured
	var list breakfixv1.InstanceList
	list.Items = make([]breakfixv1.Instance, 0, len(result.Items))
	for _, item := range result.Items {
		var inst breakfixv1.Instance
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(item.Object, &inst); err != nil {
			return nil, fmt.Errorf("convert instance item: %w", err)
		}
		list.Items = append(list.Items, inst)
	}
	return &list, nil
}

func (c *Client) DeleteInstance(ctx context.Context, ns, name string) error {
	dyn, err := c.crdClient()
	if err != nil {
		return err
	}
	return dyn.Resource(instanceGVR).Namespace(ns).Delete(ctx, name, metav1.DeleteOptions{})
}

func (c *Client) WatchInstance(ctx context.Context, ns, name, resourceVersion string) (watch.Interface, error) {
	dyn, err := c.crdClient()
	if err != nil {
		return nil, err
	}
	return dyn.Resource(instanceGVR).Namespace(ns).Watch(ctx, metav1.ListOptions{
		FieldSelector:   "metadata.name=" + name,
		ResourceVersion: resourceVersion,
		TimeoutSeconds:  ptr(int64(60)),
	})
}

func (c *Client) WatchGeneration(ctx context.Context, ns, name string) (watch.Interface, error) {
	dyn, err := c.crdClient()
	if err != nil {
		return nil, err
	}
	return dyn.Resource(generationGVR).Namespace(ns).Watch(ctx, metav1.ListOptions{
		FieldSelector:  "metadata.name=" + name,
		TimeoutSeconds: ptr(int64(300)),
	})
}

// ── Conversion helpers ──

func toUnstructured(obj runtime.Object) (*unstructured.Unstructured, error) {
	u, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return nil, err
	}
	return &unstructured.Unstructured{Object: u}, nil
}

func fromUnstructured[T any](u *unstructured.Unstructured) (T, error) {
	var result T
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &result); err != nil {
		return result, err
	}
	return result, nil
}

func ptr[T any](v T) *T { return &v }
