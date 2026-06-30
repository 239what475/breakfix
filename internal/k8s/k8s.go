// Package k8s provides a thin client-go wrapper for Pod, Exec, Job, and Namespace operations.
package k8s

import (
	"fmt"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// Client wraps the Kubernetes clientset and REST config.
type Client struct {
	clientset  *kubernetes.Clientset
	restConfig *rest.Config
}

// New creates a Client from a kubeconfig path. If the path is empty,
// it falls back to in-cluster config (service account).
func New(kubeconfig string) (*Client, error) {
	config, err := restConfig(kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("build kubeconfig: %w", err)
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("create clientset: %w", err)
	}
	return &Client{clientset: clientset, restConfig: config}, nil
}

// RESTConfig returns a copy of the underlying REST config.
func (c *Client) RESTConfig() *rest.Config {
	return rest.CopyConfig(c.restConfig)
}

// Clientset returns the underlying Kubernetes clientset.
func (c *Client) Clientset() *kubernetes.Clientset {
	return c.clientset
}

func restConfig(kubeconfig string) (*rest.Config, error) {
	if kubeconfig != "" {
		return clientcmd.BuildConfigFromFlags("", kubeconfig)
	}
	// Fall back to in-cluster config (e.g. running inside a Pod).
	inCluster, err := rest.InClusterConfig()
	if err == nil {
		return inCluster, nil
	}
	// Last resort: default loading rules.
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		clientcmd.NewDefaultClientConfigLoadingRules(),
		&clientcmd.ConfigOverrides{},
	).ClientConfig()
}
