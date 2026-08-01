package kubernetes

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

func toUnstructured(obj any) (*unstructured.Unstructured, error) {
	data, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return nil, err
	}
	return &unstructured.Unstructured{Object: data}, nil
}

func fromUnstructured[T any](obj *unstructured.Unstructured) (T, error) {
	var out T
	err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &out)
	return out, err
}
