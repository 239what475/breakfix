package agentworker

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/breakfix/breakfix/internal/agentruntime"
	"github.com/breakfix/breakfix/internal/agentserver"
)

// InternalClient keeps PostgreSQL ownership in Server while implementing the
// same Store contract used by the Worker loop.
type InternalClient struct {
	server *agentserver.Client
}

func NewInternalClient(serverURL, apiKey string) (*InternalClient, error) {
	client, err := agentserver.New(serverURL, apiKey)
	if err != nil {
		return nil, err
	}
	return &InternalClient{server: client}, nil
}

func (c *InternalClient) ClaimNext(ctx context.Context, workerID string, leaseTTL time.Duration, _ time.Time) (*agentruntime.Claim, error) {
	var response struct {
		Claim *agentruntime.Claim `json:"claim,omitempty"`
	}
	err := c.server.Post(ctx, "/api/internal/work-items/agent/claim", struct {
		WorkerID       string `json:"worker_id"`
		LeaseTTLMillis int64  `json:"lease_ttl_millis"`
	}{WorkerID: workerID, LeaseTTLMillis: leaseTTL.Milliseconds()}, &response)
	if err != nil {
		return nil, mapRuntimeError(err)
	}
	return response.Claim, nil
}

func (c *InternalClient) RenewLease(ctx context.Context, claim agentruntime.Claim, leaseTTL time.Duration, _ time.Time) error {
	return c.mutate(ctx, claim, "renew", struct {
		agentruntime.LeaseCredential
		LeaseTTLMillis int64 `json:"lease_ttl_millis"`
	}{LeaseCredential: claim.Credential(), LeaseTTLMillis: leaseTTL.Milliseconds()})
}

func (c *InternalClient) Requeue(ctx context.Context, claim agentruntime.Claim, nextRunAt time.Time, message string, _ time.Time) error {
	return c.mutate(ctx, claim, "requeue", struct {
		agentruntime.LeaseCredential
		NextRunAt time.Time `json:"next_run_at"`
		Error     string    `json:"error"`
	}{LeaseCredential: claim.Credential(), NextRunAt: nextRunAt.UTC(), Error: message})
}

func (c *InternalClient) CompleteWithMessage(ctx context.Context, claim agentruntime.Claim, message agentruntime.Message, _ time.Time) error {
	return c.mutate(ctx, claim, "complete", struct {
		agentruntime.LeaseCredential
		Message agentruntime.Message `json:"message"`
	}{LeaseCredential: claim.Credential(), Message: message})
}

func (c *InternalClient) Fail(ctx context.Context, claim agentruntime.Claim, message string, _ time.Time) error {
	return c.mutate(ctx, claim, "fail", struct {
		agentruntime.LeaseCredential
		Error string `json:"error"`
	}{LeaseCredential: claim.Credential(), Error: message})
}

func (c *InternalClient) GetRun(ctx context.Context, runID string) (*agentruntime.Run, error) {
	var response struct {
		Run agentruntime.Run `json:"run"`
	}
	if err := c.server.Post(ctx, "/api/internal/agent-runs/"+url.PathEscape(runID)+"/status", struct{}{}, &response); err != nil {
		return nil, mapRuntimeError(err)
	}
	return &response.Run, nil
}

func (c *InternalClient) mutate(ctx context.Context, claim agentruntime.Claim, operation string, body any) error {
	if c == nil || c.server == nil || !claim.Valid() {
		return errors.New("agent worker internal client requires a valid claim")
	}
	err := c.server.Post(ctx, "/api/internal/agent-runs/"+url.PathEscape(claim.Run.ID)+"/"+operation, body, nil)
	return mapRuntimeError(err)
}

func mapRuntimeError(err error) error {
	if err == nil {
		return nil
	}
	if agentserver.IsStatus(err, http.StatusConflict) {
		return agentruntime.ErrLeaseLost
	}
	if agentserver.IsStatus(err, http.StatusNotFound) {
		return agentruntime.ErrNotFound
	}
	return fmt.Errorf("agent worker server request: %w", err)
}
