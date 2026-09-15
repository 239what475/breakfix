package runtimeenvironment

import (
	"context"
	"testing"
	"time"
)

type drainClient struct {
	node, k8s               []LegacyResource
	nodeDeletes, k8sDeletes []LegacyResource
}

func (c *drainClient) ListNode(context.Context, string) ([]LegacyResource, error) {
	return append([]LegacyResource(nil), c.node...), nil
}
func (c *drainClient) ListK8s(context.Context, string) ([]LegacyResource, error) {
	return append([]LegacyResource(nil), c.k8s...), nil
}
func (c *drainClient) DeleteNode(_ context.Context, _ string, resource LegacyResource) error {
	c.nodeDeletes = append(c.nodeDeletes, resource)
	c.node = removeLegacy(c.node, resource)
	return nil
}
func (c *drainClient) DeleteK8s(_ context.Context, _ string, resource LegacyResource) error {
	c.k8sDeletes = append(c.k8sDeletes, resource)
	c.k8s = removeLegacy(c.k8s, resource)
	return nil
}

func removeLegacy(values []LegacyResource, target LegacyResource) []LegacyResource {
	result := values[:0]
	for _, value := range values {
		if value != target {
			result = append(result, value)
		}
	}
	return result
}

func TestLegacyDrainerUsesFixedNamespaceAndUIDFence(t *testing.T) {
	client := &drainClient{
		node: []LegacyResource{{Name: "node-old", UID: "uid-node"}},
		k8s:  []LegacyResource{{Name: "k8s-old", UID: "uid-k8s"}},
	}
	drainer := &LegacyDrainer{Client: client, Namespace: "breakfix-system", Retry: time.Nanosecond}
	if err := drainer.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(client.nodeDeletes) != 1 || client.nodeDeletes[0].UID != "uid-node" || len(client.k8sDeletes) != 1 || client.k8sDeletes[0].UID != "uid-k8s" {
		t.Fatalf("legacy delete fences = %#v %#v", client.nodeDeletes, client.k8sDeletes)
	}
}

func TestLegacyDrainerRejectsIncompleteIdentity(t *testing.T) {
	client := &drainClient{node: []LegacyResource{{Name: "node-old"}}}
	if err := (&LegacyDrainer{Client: client, Namespace: "breakfix-system", Retry: time.Nanosecond}).Drain(context.Background()); err == nil {
		t.Fatal("drainer accepted legacy object without UID")
	}
}
