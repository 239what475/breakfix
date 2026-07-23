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
	generationGVR           = schema.GroupVersionResource{Group: "breakfix.dev", Version: "v1", Resource: "generations"}
	containerEnvironmentGVR = schema.GroupVersionResource{Group: "breakfix.dev", Version: "v1", Resource: "containerenvironments"}
	vclusterEnvironmentGVR  = schema.GroupVersionResource{Group: "breakfix.dev", Version: "v1", Resource: "vclusterenvironments"}
	verifyTaskGVR           = schema.GroupVersionResource{Group: "breakfix.dev", Version: "v1", Resource: "verifytasks"}
)

func (c *Client) crdClient() (dynamic.Interface, error) {
	return dynamic.NewForConfig(c.restConfig)
}

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

func (c *Client) CreateContainerEnvironment(ctx context.Context, ns string, env *breakfixv1.ContainerEnvironment) (*breakfixv1.ContainerEnvironment, error) {
	env.TypeMeta = metav1.TypeMeta{APIVersion: "breakfix.dev/v1", Kind: "ContainerEnvironment"}
	return createCRD[*breakfixv1.ContainerEnvironment](ctx, c, containerEnvironmentGVR, ns, env, "create container environment")
}

func (c *Client) GetContainerEnvironment(ctx context.Context, ns, name string) (*breakfixv1.ContainerEnvironment, error) {
	return getCRD[*breakfixv1.ContainerEnvironment](ctx, c, containerEnvironmentGVR, ns, name, "get container environment")
}

func (c *Client) UpdateContainerEnvironment(ctx context.Context, ns string, env *breakfixv1.ContainerEnvironment) (*breakfixv1.ContainerEnvironment, error) {
	env.TypeMeta = metav1.TypeMeta{APIVersion: "breakfix.dev/v1", Kind: "ContainerEnvironment"}
	return updateCRD[*breakfixv1.ContainerEnvironment](ctx, c, containerEnvironmentGVR, ns, env, false, "update container environment")
}

func (c *Client) UpdateContainerEnvironmentStatus(ctx context.Context, ns string, env *breakfixv1.ContainerEnvironment) (*breakfixv1.ContainerEnvironment, error) {
	env.TypeMeta = metav1.TypeMeta{APIVersion: "breakfix.dev/v1", Kind: "ContainerEnvironment"}
	return updateCRD[*breakfixv1.ContainerEnvironment](ctx, c, containerEnvironmentGVR, ns, env, true, "update container environment status")
}

func (c *Client) ListContainerEnvironments(ctx context.Context, ns, selector string) (*breakfixv1.ContainerEnvironmentList, error) {
	return listCRD[breakfixv1.ContainerEnvironment, breakfixv1.ContainerEnvironmentList](ctx, c, containerEnvironmentGVR, ns, selector, "list container environments")
}

func (c *Client) DeleteContainerEnvironment(ctx context.Context, ns, name string) error {
	return deleteCRD(ctx, c, containerEnvironmentGVR, ns, name)
}

func (c *Client) CreateVClusterEnvironment(ctx context.Context, ns string, env *breakfixv1.VClusterEnvironment) (*breakfixv1.VClusterEnvironment, error) {
	env.TypeMeta = metav1.TypeMeta{APIVersion: "breakfix.dev/v1", Kind: "VClusterEnvironment"}
	return createCRD[*breakfixv1.VClusterEnvironment](ctx, c, vclusterEnvironmentGVR, ns, env, "create vcluster environment")
}

func (c *Client) GetVClusterEnvironment(ctx context.Context, ns, name string) (*breakfixv1.VClusterEnvironment, error) {
	return getCRD[*breakfixv1.VClusterEnvironment](ctx, c, vclusterEnvironmentGVR, ns, name, "get vcluster environment")
}

func (c *Client) UpdateVClusterEnvironment(ctx context.Context, ns string, env *breakfixv1.VClusterEnvironment) (*breakfixv1.VClusterEnvironment, error) {
	env.TypeMeta = metav1.TypeMeta{APIVersion: "breakfix.dev/v1", Kind: "VClusterEnvironment"}
	return updateCRD[*breakfixv1.VClusterEnvironment](ctx, c, vclusterEnvironmentGVR, ns, env, false, "update vcluster environment")
}

func (c *Client) UpdateVClusterEnvironmentStatus(ctx context.Context, ns string, env *breakfixv1.VClusterEnvironment) (*breakfixv1.VClusterEnvironment, error) {
	env.TypeMeta = metav1.TypeMeta{APIVersion: "breakfix.dev/v1", Kind: "VClusterEnvironment"}
	return updateCRD[*breakfixv1.VClusterEnvironment](ctx, c, vclusterEnvironmentGVR, ns, env, true, "update vcluster environment status")
}

func (c *Client) ListVClusterEnvironments(ctx context.Context, ns, selector string) (*breakfixv1.VClusterEnvironmentList, error) {
	return listCRD[breakfixv1.VClusterEnvironment, breakfixv1.VClusterEnvironmentList](ctx, c, vclusterEnvironmentGVR, ns, selector, "list vcluster environments")
}

func (c *Client) DeleteVClusterEnvironment(ctx context.Context, ns, name string) error {
	return deleteCRD(ctx, c, vclusterEnvironmentGVR, ns, name)
}

func (c *Client) CreateVerifyTask(ctx context.Context, ns string, task *breakfixv1.VerifyTask) (*breakfixv1.VerifyTask, error) {
	task.TypeMeta = metav1.TypeMeta{APIVersion: "breakfix.dev/v1", Kind: "VerifyTask"}
	return createCRD[*breakfixv1.VerifyTask](ctx, c, verifyTaskGVR, ns, task, "create verify task")
}

func (c *Client) GetVerifyTask(ctx context.Context, ns, name string) (*breakfixv1.VerifyTask, error) {
	return getCRD[*breakfixv1.VerifyTask](ctx, c, verifyTaskGVR, ns, name, "get verify task")
}

func (c *Client) UpdateVerifyTask(ctx context.Context, ns string, task *breakfixv1.VerifyTask) (*breakfixv1.VerifyTask, error) {
	task.TypeMeta = metav1.TypeMeta{APIVersion: "breakfix.dev/v1", Kind: "VerifyTask"}
	return updateCRD[*breakfixv1.VerifyTask](ctx, c, verifyTaskGVR, ns, task, false, "update verify task")
}

func (c *Client) UpdateVerifyTaskStatus(ctx context.Context, ns string, task *breakfixv1.VerifyTask) (*breakfixv1.VerifyTask, error) {
	task.TypeMeta = metav1.TypeMeta{APIVersion: "breakfix.dev/v1", Kind: "VerifyTask"}
	return updateCRD[*breakfixv1.VerifyTask](ctx, c, verifyTaskGVR, ns, task, true, "update verify task status")
}

func (c *Client) DeleteVerifyTask(ctx context.Context, ns, name string) error {
	return deleteCRD(ctx, c, verifyTaskGVR, ns, name)
}

func (c *Client) WatchVerifyTask(ctx context.Context, ns, name string) (watch.Interface, error) {
	dyn, err := c.crdClient()
	if err != nil {
		return nil, err
	}
	return dyn.Resource(verifyTaskGVR).Namespace(ns).Watch(ctx, metav1.ListOptions{
		FieldSelector:  "metadata.name=" + name,
		TimeoutSeconds: ptr(int64(300)),
	})
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
	case *breakfixv1.ContainerEnvironmentList:
		typed.Items = make([]breakfixv1.ContainerEnvironment, 0, len(result.Items))
		for _, item := range result.Items {
			var env breakfixv1.ContainerEnvironment
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(item.Object, &env); err != nil {
				return nil, fmt.Errorf("convert container environment item: %w", err)
			}
			typed.Items = append(typed.Items, env)
		}
	case *breakfixv1.VClusterEnvironmentList:
		typed.Items = make([]breakfixv1.VClusterEnvironment, 0, len(result.Items))
		for _, item := range result.Items {
			var env breakfixv1.VClusterEnvironment
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(item.Object, &env); err != nil {
				return nil, fmt.Errorf("convert vcluster environment item: %w", err)
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
	return dyn.Resource(gvr).Namespace(ns).Delete(ctx, name, metav1.DeleteOptions{})
}
