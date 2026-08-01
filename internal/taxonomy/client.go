package taxonomy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/adapter/internalapi"
	app "github.com/breakfix/breakfix/internal/application/taxonomy"
	domain "github.com/breakfix/breakfix/internal/domain/taxonomy"
)

// Client is the taxonomy-worker's complete durable boundary. It only talks to
// Server and therefore never receives a PostgreSQL DSN or taxonomy path.
type Client struct{ server *internalapi.Client }

func NewClient(serverURL, apiKey string) (*Client, error) {
	server, err := internalapi.New(serverURL, apiKey)
	if err != nil {
		return nil, err
	}
	return &Client{server: server}, nil
}

func (c *Client) Claim(ctx context.Context, workerID string, leaseTTL time.Duration) (*domain.Claim, error) {
	var response struct {
		Claim *domain.Claim `json:"claim,omitempty"`
	}
	err := c.post(ctx, "/api/internal/taxonomy-workflows/claim", struct {
		WorkerID       string `json:"worker_id"`
		LeaseTTLMillis int64  `json:"lease_ttl_millis"`
	}{WorkerID: workerID, LeaseTTLMillis: leaseTTL.Milliseconds()}, &response)
	if err != nil {
		return nil, err
	}
	if response.Claim != nil && !response.Claim.Valid() {
		return nil, errors.New("server returned an invalid taxonomy workflow claim")
	}
	return response.Claim, nil
}

func (c *Client) Renew(ctx context.Context, claim domain.Claim, leaseTTL time.Duration) error {
	if !claim.Valid() {
		return errors.New("renew taxonomy workflow requires a valid claim")
	}
	return c.post(ctx, workflowPath(claim.Workflow.ID, "renew"), struct {
		domain.LeaseCredential
		LeaseTTLMillis int64 `json:"lease_ttl_millis"`
	}{LeaseCredential: claim.LeaseCredential, LeaseTTLMillis: leaseTTL.Milliseconds()}, nil)
}

func (c *Client) Context(ctx context.Context, claim domain.Claim) (*app.Context, error) {
	if !claim.Valid() {
		return nil, errors.New("taxonomy context requires a valid claim")
	}
	var response struct {
		Context app.Context `json:"context"`
	}
	if err := c.postLong(ctx, workflowPath(claim.Workflow.ID, "context"), claim.LeaseCredential, &response); err != nil {
		return nil, err
	}
	if !response.Context.ValidFor(claim) {
		return nil, errors.New("server returned invalid taxonomy workflow context")
	}
	return &response.Context, nil
}

func (c *Client) StartAgentRun(ctx context.Context, claim domain.Claim, role app.AgentRole, model string) (*app.StartAgentRunResponse, error) {
	if !claim.Valid() {
		return nil, errors.New("start taxonomy agent run requires a valid claim")
	}
	request := app.StartAgentRunRequest{LeaseCredential: claim.LeaseCredential, ExpectedState: claim.Workflow.State, Role: role, Model: model}
	if err := request.Validate(claim.Workflow.ID); err != nil {
		return nil, err
	}
	var response app.StartAgentRunResponse
	if err := c.post(ctx, workflowPath(claim.Workflow.ID, "agent-runs"), request, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func (c *Client) Phase(ctx context.Context, claim domain.Claim, request app.PhaseRequest) (*domain.Claim, error) {
	if !claim.Valid() {
		return nil, errors.New("taxonomy phase requires a valid claim")
	}
	request.LeaseCredential = claim.LeaseCredential
	request.ExpectedState = claim.Workflow.State
	if err := request.Validate(); err != nil {
		return nil, err
	}
	var response struct {
		Claim *domain.Claim `json:"claim,omitempty"`
	}
	if err := c.postLong(ctx, workflowPath(claim.Workflow.ID, "phase"), request, &response); err != nil {
		return nil, err
	}
	if response.Claim != nil && !response.Claim.Valid() {
		return nil, errors.New("server returned an invalid taxonomy phase claim")
	}
	return response.Claim, nil
}

func (c *Client) post(ctx context.Context, path string, body, output any) error {
	if c == nil || c.server == nil {
		return errors.New("taxonomy worker client is not configured")
	}
	return mapClientError(c.server.Post(ctx, path, body, output))
}

func (c *Client) postLong(ctx context.Context, path string, body, output any) error {
	if c == nil || c.server == nil {
		return errors.New("taxonomy worker client is not configured")
	}
	return mapClientError(c.server.PostLong(ctx, path, body, output))
}

func workflowPath(id, suffix string) string {
	return "/api/internal/taxonomy-workflows/" + url.PathEscape(strings.TrimSpace(id)) + "/" + suffix
}

func mapClientError(err error) error {
	if err == nil {
		return nil
	}
	if internalapi.IsStatus(err, http.StatusConflict) {
		return domain.ErrLeaseLost
	}
	if internalapi.IsStatus(err, http.StatusNotFound) {
		return domain.ErrWorkflowNotFound
	}
	return fmt.Errorf("taxonomy worker server request: %w", err)
}
