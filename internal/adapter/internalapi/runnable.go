package internalapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
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
	if err := c.post(ctx, "/api/internal/runnable-actions/claim", struct {
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
	return c.post(ctx, "/api/internal/runnable-actions/renew", struct {
		Credential     runnable.LeaseCredential `json:"credential"`
		LeaseTTLMillis int64                    `json:"lease_ttl_millis"`
	}{credential, leaseTTL.Milliseconds()}, nil)
}

// ReadSource returns only the immutable archive bound to this materialization
// lease. The Worker verifies the returned bytes before handing them to a
// provider, and this client repeats the check before returning them.
func (c *RunnableActionClient) ReadSource(ctx context.Context, credential runnable.LeaseCredential, source runnable.SourceArchive) ([]byte, error) {
	if c == nil || c.server == nil || credential.Validate() != nil || credential.Identity.Phase != runnable.ActionMaterializeArtifact || source.Validate() != nil {
		return nil, errors.New("runnable source request is invalid")
	}
	var response struct {
		Archive []byte `json:"archive"`
	}
	if err := c.postLong(ctx, "/api/internal/runnable-actions/source", struct {
		Credential runnable.LeaseCredential `json:"credential"`
	}{credential}, &response); err != nil {
		return nil, err
	}
	sum := sha256.Sum256(response.Archive)
	if actual := "sha256:" + hex.EncodeToString(sum[:]); actual != source.Digest {
		return nil, runnable.NewArtifactFailure("source-archive-digest", "Server returned source bytes for another runnable spec")
	}
	return response.Archive, nil
}

// StoreExecutionOutput writes provider stdout and stderr under the current
// verification lease. The Server owns the immutable object namespace; the
// client verifies that its returned reference is exactly the capture supplied.
func (c *RunnableActionClient) StoreExecutionOutput(ctx context.Context, credential runnable.LeaseCredential, capture runnable.OutputCapture) (runnable.ImmutableReference, error) {
	if c == nil || c.server == nil || credential.Validate() != nil || credential.Identity.Phase != runnable.ActionVerify || capture.Validate() != nil {
		return runnable.ImmutableReference{}, errors.New("runnable execution output request is invalid")
	}
	expected, _, err := capture.Reference()
	if err != nil {
		return runnable.ImmutableReference{}, err
	}
	var response struct {
		Reference runnable.ImmutableReference `json:"reference"`
	}
	if err := c.post(ctx, "/api/internal/runnable-actions/output", struct {
		Credential runnable.LeaseCredential `json:"credential"`
		Capture    runnable.OutputCapture   `json:"capture"`
	}{credential, capture}, &response); err != nil {
		return runnable.ImmutableReference{}, err
	}
	if response.Reference != expected {
		return runnable.ImmutableReference{}, errors.New("server returned an execution output reference for another capture")
	}
	return response.Reference, nil
}

func (c *RunnableActionClient) CompleteMaterialization(ctx context.Context, credential runnable.LeaseCredential, revision runnable.StoredRevision) error {
	if c == nil || c.server == nil || credential.Identity.Phase != runnable.ActionMaterializeArtifact || credential.Validate() != nil || revision.Validate() != nil {
		return errors.New("runnable materialization completion is invalid")
	}
	return c.postLong(ctx, "/api/internal/runnable-actions/materialization/complete", struct {
		Credential runnable.LeaseCredential `json:"credential"`
		Revision   runnable.StoredRevision  `json:"revision"`
	}{credential, revision}, nil)
}

func (c *RunnableActionClient) CompleteVerification(ctx context.Context, credential runnable.LeaseCredential, report runnable.StoredVerificationReport) error {
	if c == nil || c.server == nil || credential.Identity.Phase != runnable.ActionVerify || credential.Validate() != nil || report.Validate() != nil {
		return errors.New("runnable verification completion is invalid")
	}
	return c.postLong(ctx, "/api/internal/runnable-actions/verification/complete", struct {
		Credential runnable.LeaseCredential          `json:"credential"`
		Report     runnable.StoredVerificationReport `json:"report"`
	}{credential, report}, nil)
}

func (c *RunnableActionClient) ReportFailure(ctx context.Context, credential runnable.LeaseCredential, class runnable.FailureClass, code, summary string) error {
	if c == nil || c.server == nil || credential.Validate() != nil || !class.Valid() || strings.TrimSpace(code) == "" || strings.TrimSpace(summary) == "" {
		return errors.New("runnable action failure is invalid")
	}
	return c.post(ctx, "/api/internal/runnable-actions/failure", struct {
		Credential runnable.LeaseCredential `json:"credential"`
		Class      runnable.FailureClass    `json:"class"`
		Code       string                   `json:"code"`
		Summary    string                   `json:"summary"`
	}{credential, class, code, summary}, nil)
}

func (c *RunnableActionClient) post(ctx context.Context, path string, body, output any) error {
	if c == nil || c.server == nil {
		return errors.New("runnable action client is not configured")
	}
	return mapRunnableActionError(c.server.Post(ctx, path, body, output))
}

func (c *RunnableActionClient) postLong(ctx context.Context, path string, body, output any) error {
	if c == nil || c.server == nil {
		return errors.New("runnable action client is not configured")
	}
	return mapRunnableActionError(c.server.PostLong(ctx, path, body, output))
}

func mapRunnableActionError(err error) error {
	if err == nil {
		return nil
	}
	if IsStatus(err, http.StatusConflict) {
		return runnable.ErrActionLeaseLost
	}
	if IsStatus(err, http.StatusNotFound) {
		return runnable.ErrSourceNotFound
	}
	return fmt.Errorf("runnable worker server request: %w", err)
}
