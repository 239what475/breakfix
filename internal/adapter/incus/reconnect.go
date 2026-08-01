package incus

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// ReconnectableClient keeps Node-capable processes available while Incus is
// temporarily unreachable. It connects and runs the role preflight lazily;
// an unavailable transport is discarded so a later operation establishes a
// fresh connection. Callers still receive the original typed provider errors.
//
// This is deliberately an internal adapter rather than a second provider
// implementation. Resource ownership, request validation, and all Incus API
// operations remain in Client.
type ReconnectableClient struct {
	config Config
	role   Role

	mu     sync.Mutex
	client *Client
}

func NewReconnectableClient(config Config, role Role) (*ReconnectableClient, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if !role.Valid() {
		return nil, fmt.Errorf("%w: invalid Incus provider role %q", ErrInvalid, role)
	}
	return &ReconnectableClient{config: config, role: role}, nil
}

func (c *ReconnectableClient) Close() {
	if c == nil {
		return
	}
	c.mu.Lock()
	client := c.client
	c.client = nil
	c.mu.Unlock()
	if client != nil {
		client.Close()
	}
}

// Preflight verifies that this role can use the configured Incus provider.
// It deliberately goes through the reconnectable path so an unavailable
// transport is discarded and the next probe or operation gets a fresh client.
func (c *ReconnectableClient) Preflight(ctx context.Context) (PreflightResult, error) {
	var result PreflightResult
	err := c.use(ctx, func(client *Client) error {
		var err error
		result, err = client.Preflight(ctx, c.role)
		return err
	})
	return result, err
}

// NodeEnvironmentIdentity is fully deterministic and does not require an
// active provider connection. Persisting it before the first provision call
// gives a disconnected NodeEnvironment a stable, inspectable identity.
func (c *ReconnectableClient) NodeEnvironmentIdentity(environmentUID string, logicalNames []string) (NodeEnvironmentIdentity, error) {
	if c == nil {
		return NodeEnvironmentIdentity{}, fmt.Errorf("%w: Node provider is not configured", ErrUnavailable)
	}
	return IdentityForNodeEnvironment(c.config.NamePrefix, environmentUID, logicalNames)
}

func (c *ReconnectableClient) ProvisionNodeEnvironment(ctx context.Context, request ProvisionNodeEnvironmentRequest) (NodeEnvironmentObservation, error) {
	var result NodeEnvironmentObservation
	err := c.use(ctx, func(client *Client) error {
		var err error
		result, err = client.ProvisionNodeEnvironment(ctx, request)
		return err
	})
	return result, err
}

func (c *ReconnectableClient) ObserveNodeEnvironment(ctx context.Context, request ProvisionNodeEnvironmentRequest) (NodeEnvironmentObservation, error) {
	var result NodeEnvironmentObservation
	err := c.use(ctx, func(client *Client) error {
		var err error
		result, err = client.ObserveNodeEnvironment(ctx, request)
		return err
	})
	return result, err
}

func (c *ReconnectableClient) DeleteNodeEnvironment(ctx context.Context, request ProvisionNodeEnvironmentRequest) error {
	return c.use(ctx, func(client *Client) error {
		return client.DeleteNodeEnvironment(ctx, request)
	})
}

func (c *ReconnectableClient) BuildNodeImage(ctx context.Context, request BuildNodeImageRequest) (BuildNodeImageResult, error) {
	var result BuildNodeImageResult
	err := c.use(ctx, func(client *Client) error {
		var err error
		result, err = client.BuildNodeImage(ctx, request)
		return err
	})
	return result, err
}

func (c *ReconnectableClient) PublishNodeImage(ctx context.Context, request PublishNodeImageRequest) (PublishNodeImageResult, error) {
	var result PublishNodeImageResult
	err := c.use(ctx, func(client *Client) error {
		var err error
		result, err = client.PublishNodeImage(ctx, request)
		return err
	})
	return result, err
}

func (c *ReconnectableClient) PublishChallengeNodeImage(ctx context.Context, request PublishChallengeNodeImageRequest) (PublishNodeImageResult, error) {
	var result PublishNodeImageResult
	err := c.use(ctx, func(client *Client) error {
		var err error
		result, err = client.PublishChallengeNodeImage(ctx, request)
		return err
	})
	return result, err
}

func (c *ReconnectableClient) FindChallengeNodeImage(ctx context.Context, challengeID, revision string) (PublishNodeImageResult, bool, error) {
	var result PublishNodeImageResult
	var found bool
	err := c.use(ctx, func(client *Client) error {
		var err error
		result, found, err = client.FindChallengeNodeImage(ctx, challengeID, revision)
		return err
	})
	return result, found, err
}

func (c *ReconnectableClient) DeleteBuildNodeImage(ctx context.Context, result BuildNodeImageResult) error {
	return c.use(ctx, func(client *Client) error {
		return client.DeleteBuildNodeImage(ctx, result)
	})
}

func (c *ReconnectableClient) DeleteBuildNodeImageAttempt(ctx context.Context, workflowID string, attempt int64) error {
	return c.use(ctx, func(client *Client) error {
		return client.DeleteBuildNodeImageAttempt(ctx, workflowID, attempt)
	})
}

func (c *ReconnectableClient) DeleteCandidateNodeImage(ctx context.Context, candidateRevisionID, fingerprint string) error {
	return c.use(ctx, func(client *Client) error {
		return client.DeleteCandidateNodeImage(ctx, candidateRevisionID, fingerprint)
	})
}

func (c *ReconnectableClient) DeleteChallengeNodeImage(ctx context.Context, challengeID, fingerprint string) error {
	return c.use(ctx, func(client *Client) error {
		return client.DeleteChallengeNodeImage(ctx, challengeID, fingerprint)
	})
}

func (c *ReconnectableClient) ExecNode(ctx context.Context, request ExecNodeRequest) (ExecNodeResult, error) {
	var result ExecNodeResult
	err := c.use(ctx, func(client *Client) error {
		var err error
		result, err = client.ExecNode(ctx, request)
		return err
	})
	return result, err
}

func (c *ReconnectableClient) ExecNodePTY(ctx context.Context, request ExecNodePTYRequest) error {
	return c.use(ctx, func(client *Client) error {
		return client.ExecNodePTY(ctx, request)
	})
}

func (c *ReconnectableClient) CloseNodePTYWindow(ctx context.Context, request CloseNodePTYWindowRequest) error {
	return c.use(ctx, func(client *Client) error {
		return client.CloseNodePTYWindow(ctx, request)
	})
}

func (c *ReconnectableClient) use(ctx context.Context, operation func(*Client) error) error {
	client, err := c.clientFor(ctx)
	if err != nil {
		return err
	}
	err = operation(client)
	if errors.Is(err, ErrUnavailable) {
		c.invalidate(client)
	}
	return err
}

func (c *ReconnectableClient) clientFor(ctx context.Context) (*Client, error) {
	if c == nil {
		return nil, fmt.Errorf("%w: Node provider is not configured", ErrUnavailable)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client != nil {
		return c.client, nil
	}
	client, err := Connect(ctx, c.config)
	if err == nil {
		_, err = client.Preflight(ctx, c.role)
	}
	if err != nil {
		if client != nil {
			client.Close()
		}
		if errors.Is(err, ErrUnavailable) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: initialize %s provider: %v", ErrUnavailable, c.role, err)
	}
	c.client = client
	return client, nil
}

func (c *ReconnectableClient) invalidate(client *Client) {
	if c == nil || client == nil {
		return
	}
	c.mu.Lock()
	if c.client != client {
		c.mu.Unlock()
		return
	}
	c.client = nil
	c.mu.Unlock()
	client.Close()
}
