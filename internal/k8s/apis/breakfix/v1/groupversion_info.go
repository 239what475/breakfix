package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	Group   = "breakfix.dev"
	Version = "v1"
)

var (
	SchemeGroupVersion = schema.GroupVersion{Group: Group, Version: Version}
	SchemeBuilder      = runtime.NewSchemeBuilder(addKnownTypes)
	AddToScheme        = SchemeBuilder.AddToScheme
)

func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(SchemeGroupVersion,
		&ContainerEnvironment{}, &ContainerEnvironmentList{},
		&VClusterEnvironment{}, &VClusterEnvironmentList{},
		&NodeEnvironment{}, &NodeEnvironmentList{},
		&VK8sEnvironment{}, &VK8sEnvironmentList{},
		&VerifyTask{}, &VerifyTaskList{},
	)
	metav1.AddToGroupVersion(scheme, SchemeGroupVersion)
	return nil
}
