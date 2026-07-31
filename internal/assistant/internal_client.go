package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/breakfix/breakfix/internal/agentruntime"
	"github.com/breakfix/breakfix/internal/agentserver"
)

// RuntimeClient is the narrow Worker-to-Server interface for Assistant runs.
// It intentionally exposes no Kubernetes, filesystem, or credential details.
type RuntimeClient interface {
	LoadContext(context.Context, agentruntime.Claim) (ExecutionContext, error)
	InvokeTool(context.Context, agentruntime.Claim, string, any, any) error
	PublishEvent(context.Context, agentruntime.Claim, Event) error
}

// InternalClient calls the Server ClusterIP internal API. Every request is
// authenticated with the deployment-injected internal key and fenced by the
// supplied attempt lease.
type InternalClient struct {
	server *agentserver.Client
}

func NewInternalClient(serverURL, apiKey string) (*InternalClient, error) {
	server, err := agentserver.New(serverURL, apiKey)
	if err != nil {
		return nil, err
	}
	return &InternalClient{server: server}, nil
}

func (c *InternalClient) LoadContext(ctx context.Context, claim agentruntime.Claim) (ExecutionContext, error) {
	var result ExecutionContext
	err := c.post(ctx, claim.Run.ID, "/assistant/context", claim.Credential(), &result)
	return result, err
}

func (c *InternalClient) InvokeTool(ctx context.Context, claim agentruntime.Claim, name string, arguments any, result any) error {
	data, err := json.Marshal(arguments)
	if err != nil {
		return fmt.Errorf("encode assistant tool arguments: %w", err)
	}
	return c.post(ctx, claim.Run.ID, "/assistant/tools/"+url.PathEscape(name), InternalToolRequest{
		LeaseCredential: claim.Credential(),
		Arguments:       data,
	}, result)
}

func (c *InternalClient) PublishEvent(ctx context.Context, claim agentruntime.Claim, event Event) error {
	if event.Type != "delta" && event.Type != "tool" && event.Type != "reset" {
		return fmt.Errorf("unsupported assistant event type %q", event.Type)
	}
	return c.post(ctx, claim.Run.ID, "/assistant/events", InternalEventRequest{
		LeaseCredential: claim.Credential(),
		Type:            event.Type,
		Content:         event.Content,
		Tool:            event.Tool,
	}, nil)
}

func (c *InternalClient) post(ctx context.Context, runID, suffix string, body any, output any) error {
	if c == nil || c.server == nil {
		return fmt.Errorf("assistant internal client is not configured")
	}
	return c.server.Post(ctx, "/api/internal/agent-runs/"+url.PathEscape(runID)+suffix, body, output)
}
