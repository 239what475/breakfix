package internalapi

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/domain/runnable"
)

// RunnableActionClient is the public Runtime Worker's Server boundary. It
// carries only generic runnable contracts and never exposes publication state.
type RunnableActionClient struct{ server *Client }

func NewRunnableActionClient(serverURL, apiKey string) (*RunnableActionClient, error) {
	server, err := New(serverURL, apiKey)
	if err != nil {
		return nil, err
	}
	return &RunnableActionClient{server: server}, nil
}

func (c *RunnableActionClient) Claim(ctx context.Context, workerID string, leaseTTL time.Duration) (*runnable.ActionContext, error) {
	if c == nil || c.server == nil || strings.TrimSpace(workerID) == "" || leaseTTL <= 0 {
		return nil, errors.New("runnable action claim is invalid")
	}
	var response struct {
		Action *runnable.ActionContext `json:"action,omitempty"`
	}
	if err := c.server.Post(ctx, "/api/internal/runnable-actions/claim", struct {
		WorkerID       string `json:"worker_id"`
		LeaseTTLMillis int64  `json:"lease_ttl_millis"`
	}{workerID, leaseTTL.Milliseconds()}, &response); err != nil {
		return nil, err
	}
	if response.Action != nil {
		if err := response.Action.Validate(); err != nil {
			return nil, fmt.Errorf("server returned an invalid runnable action: %w", err)
		}
	}
	return response.Action, nil
}

func (c *RunnableActionClient) Renew(ctx context.Context, credential runnable.LeaseCredential, leaseTTL time.Duration) error {
	if c == nil || c.server == nil || credential.Validate() != nil || leaseTTL <= 0 {
		return errors.New("runnable action renewal is invalid")
	}
	return c.server.Post(ctx, "/api/internal/runnable-actions/renew", struct {
		Credential     runnable.LeaseCredential `json:"credential"`
		LeaseTTLMillis int64                    `json:"lease_ttl_millis"`
	}{credential, leaseTTL.Milliseconds()}, nil)
}

func (c *RunnableActionClient) CompleteMaterialization(ctx context.Context, credential runnable.LeaseCredential, revision runnable.StoredRevision) error {
	if c == nil || c.server == nil || credential.Identity.Phase != runnable.ActionMaterializeArtifact || credential.Validate() != nil || revision.Validate() != nil {
		return errors.New("runnable materialization completion is invalid")
	}
	return c.server.PostLong(ctx, "/api/internal/runnable-actions/materialization/complete", struct {
		Credential runnable.LeaseCredential `json:"credential"`
		Revision   runnable.StoredRevision  `json:"revision"`
	}{credential, revision}, nil)
}

func (c *RunnableActionClient) CompleteVerification(ctx context.Context, credential runnable.LeaseCredential, report runnable.StoredVerificationReport) error {
	if c == nil || c.server == nil || credential.Identity.Phase != runnable.ActionVerify || credential.Validate() != nil || report.Validate() != nil {
		return errors.New("runnable verification completion is invalid")
	}
	return c.server.PostLong(ctx, "/api/internal/runnable-actions/verification/complete", struct {
		Credential runnable.LeaseCredential          `json:"credential"`
		Report     runnable.StoredVerificationReport `json:"report"`
	}{credential, report}, nil)
}

func (c *RunnableActionClient) ReportFailure(ctx context.Context, credential runnable.LeaseCredential, class runnable.FailureClass, code, summary string) error {
	if c == nil || c.server == nil || credential.Validate() != nil || !class.Valid() || strings.TrimSpace(code) == "" || strings.TrimSpace(summary) == "" {
		return errors.New("runnable action failure is invalid")
	}
	return c.server.Post(ctx, "/api/internal/runnable-actions/failure", struct {
		Credential runnable.LeaseCredential `json:"credential"`
		Class      runnable.FailureClass    `json:"class"`
		Code       string                   `json:"code"`
		Summary    string                   `json:"summary"`
	}{credential, class, code, summary}, nil)
}
