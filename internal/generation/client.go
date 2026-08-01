package generation

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/agentserver"
	app "github.com/breakfix/breakfix/internal/application/generation"
	domain "github.com/breakfix/breakfix/internal/domain/generation"
)

// Client is the Generate Worker's complete Server boundary. It intentionally
// exposes one workflow lease rather than the former per-stage task queue.
type Client struct {
	server *agentserver.Client
}

func NewClient(serverURL, apiKey string) (*Client, error) {
	server, err := agentserver.New(serverURL, apiKey)
	if err != nil {
		return nil, err
	}
	return &Client{server: server}, nil
}

func (c *Client) Claim(ctx context.Context, workerID string, leaseTTL time.Duration) (*domain.Claim, error) {
	var response struct {
		Claim *domain.Claim `json:"claim,omitempty"`
	}
	if err := c.post(ctx, "/api/internal/generation-workflows/claim", struct {
		WorkerID       string `json:"worker_id"`
		LeaseTTLMillis int64  `json:"lease_ttl_millis"`
	}{WorkerID: workerID, LeaseTTLMillis: leaseTTL.Milliseconds()}, &response); err != nil {
		return nil, err
	}
	if response.Claim != nil && !response.Claim.Valid() {
		return nil, errors.New("server returned an invalid generation workflow claim")
	}
	return response.Claim, nil
}

func (c *Client) Renew(ctx context.Context, claim domain.Claim, leaseTTL time.Duration) error {
	if !claim.Valid() {
		return errors.New("renew generation workflow requires a valid claim")
	}
	return c.post(ctx, generationPath(claim.Workflow.ID, "renew"), struct {
		domain.LeaseCredential
		LeaseTTLMillis int64 `json:"lease_ttl_millis"`
	}{LeaseCredential: claim.LeaseCredential, LeaseTTLMillis: leaseTTL.Milliseconds()}, nil)
}

func (c *Client) Context(ctx context.Context, claim domain.Claim) (*domain.Context, error) {
	if !claim.Valid() {
		return nil, errors.New("generation workflow context requires a valid claim")
	}
	var response struct {
		Context domain.Context `json:"context"`
	}
	if err := c.postLong(ctx, generationPath(claim.Workflow.ID, "context"), claim.LeaseCredential, &response); err != nil {
		return nil, err
	}
	if response.Context.Workflow.ID != claim.Workflow.ID || response.Context.Workflow.State != claim.Workflow.State {
		return nil, errors.New("server returned generation context for a different workflow state")
	}
	return &response.Context, nil
}

func (c *Client) StartAgentRun(ctx context.Context, claim domain.Claim, request app.StartAgentRunRequest) (*app.StartAgentRunResponse, error) {
	if !claim.Valid() {
		return nil, errors.New("start generation agent run requires a valid claim")
	}
	request.LeaseCredential = claim.LeaseCredential
	request.ExpectedState = claim.Workflow.State
	if err := request.Validate(claim.Workflow.ID); err != nil {
		return nil, err
	}
	var response app.StartAgentRunResponse
	if err := c.post(ctx, generationPath(claim.Workflow.ID, "agent-runs"), request, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

// Phase atomically records one typed stage result and returns the refreshed
// lease view. A failure report that releases the lease returns a nil claim.
func (c *Client) Phase(ctx context.Context, claim domain.Claim, request app.PhaseRequest) (*domain.Claim, error) {
	if !claim.Valid() {
		return nil, errors.New("generation workflow phase requires a valid claim")
	}
	request.LeaseCredential = claim.LeaseCredential
	request.ExpectedState = claim.Workflow.State
	if err := request.Validate(); err != nil {
		return nil, err
	}
	var response struct {
		Claim *domain.Claim `json:"claim,omitempty"`
	}
	if err := c.postLong(ctx, generationPath(claim.Workflow.ID, "phase"), request, &response); err != nil {
		return nil, err
	}
	if response.Claim != nil && !response.Claim.Valid() {
		return nil, errors.New("server returned an invalid generation phase claim")
	}
	return response.Claim, nil
}

func (c *Client) CandidateArchive(ctx context.Context, claim domain.Claim) ([]byte, string, error) {
	var response struct {
		Archive []byte `json:"archive"`
		SHA256  string `json:"sha256"`
	}
	if err := c.postLong(ctx, generationPath(claim.Workflow.ID, "candidate/archive"), claim.LeaseCredential, &response); err != nil {
		return nil, "", err
	}
	return response.Archive, response.SHA256, nil
}

func (c *Client) K8sBase(ctx context.Context, claim domain.Claim) ([]byte, string, error) {
	var response struct {
		Archive []byte `json:"archive"`
		SHA256  string `json:"sha256"`
	}
	if err := c.postLong(ctx, generationPath(claim.Workflow.ID, "k8s/base"), claim.LeaseCredential, &response); err != nil {
		return nil, "", err
	}
	return response.Archive, response.SHA256, nil
}

func (c *Client) BuildArchive(ctx context.Context, claim domain.Claim) ([]byte, string, error) {
	var response struct {
		Archive []byte `json:"archive"`
		SHA256  string `json:"sha256"`
	}
	if err := c.postLong(ctx, generationPath(claim.Workflow.ID, "build/archive"), claim.LeaseCredential, &response); err != nil {
		return nil, "", err
	}
	return response.Archive, response.SHA256, nil
}

func (c *Client) post(ctx context.Context, path string, body, output any) error {
	if c == nil || c.server == nil {
		return errors.New("generation worker client is not configured")
	}
	return mapClientError(c.server.Post(ctx, path, body, output))
}

func (c *Client) postLong(ctx context.Context, path string, body, output any) error {
	if c == nil || c.server == nil {
		return errors.New("generation worker client is not configured")
	}
	return mapClientError(c.server.PostLong(ctx, path, body, output))
}

func generationPath(id, suffix string) string {
	return "/api/internal/generation-workflows/" + url.PathEscape(strings.TrimSpace(id)) + "/" + suffix
}

func mapClientError(err error) error {
	if err == nil {
		return nil
	}
	if agentserver.IsStatus(err, http.StatusConflict) {
		return domain.ErrLeaseLost
	}
	if agentserver.IsStatus(err, http.StatusNotFound) {
		return domain.ErrWorkflowNotFound
	}
	return fmt.Errorf("generation worker server request: %w", err)
}
