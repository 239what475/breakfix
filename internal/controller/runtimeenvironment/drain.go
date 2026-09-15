package runtimeenvironment

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// LegacyResource is the minimum identity needed to fence deletion of a v1
// environment. The UID precondition prevents a name-reused object from being
// deleted after the drain has observed an earlier object.
type LegacyResource struct {
	Name string
	UID  string
}

// LegacyDrainClient exposes only the old environment collections. It is kept
// separate from the v2 client so the drain cannot accidentally create or
// update legacy objects.
type LegacyDrainClient interface {
	ListNode(context.Context, string) ([]LegacyResource, error)
	ListK8s(context.Context, string) ([]LegacyResource, error)
	DeleteNode(context.Context, string, LegacyResource) error
	DeleteK8s(context.Context, string, LegacyResource) error
}

// LegacyDrainer removes every legacy object from one fixed namespace. A
// second scan is required after each deletion because list results are not a
// transaction and deletion may be asynchronous behind finalizers.
type LegacyDrainer struct {
	Client    LegacyDrainClient
	Namespace string
	Retry     time.Duration
	Now       func() time.Time
}

func (d *LegacyDrainer) Drain(ctx context.Context) error {
	if d == nil || d.Client == nil || strings.TrimSpace(d.Namespace) == "" {
		return errors.New("legacy environment drainer requires client and namespace")
	}
	retry := d.Retry
	if retry <= 0 {
		retry = time.Second
	}
	for {
		node, err := d.Client.ListNode(ctx, d.Namespace)
		if err != nil {
			return fmt.Errorf("list legacy Node environments: %w", err)
		}
		k8s, err := d.Client.ListK8s(ctx, d.Namespace)
		if err != nil {
			return fmt.Errorf("list legacy K8s environments: %w", err)
		}
		if len(node) == 0 && len(k8s) == 0 {
			return nil
		}
		for _, resource := range node {
			if strings.TrimSpace(resource.Name) == "" || strings.TrimSpace(resource.UID) == "" {
				return errors.New("legacy Node environment is missing name or UID")
			}
			if err := d.Client.DeleteNode(ctx, d.Namespace, resource); err != nil {
				return fmt.Errorf("delete legacy Node environment %q: %w", resource.Name, err)
			}
		}
		for _, resource := range k8s {
			if strings.TrimSpace(resource.Name) == "" || strings.TrimSpace(resource.UID) == "" {
				return errors.New("legacy K8s environment is missing name or UID")
			}
			if err := d.Client.DeleteK8s(ctx, d.Namespace, resource); err != nil {
				return fmt.Errorf("delete legacy K8s environment %q: %w", resource.Name, err)
			}
		}
		timer := time.NewTimer(retry)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
