package internalapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	domain "github.com/breakfix/breakfix/internal/domain/generation"
)

// GenerationWorkflowClient is the Runtime Worker's complete Server boundary.
// It only exposes one lease-fenced RuntimeAction at a time; it cannot read an
// authoring Plan, workspace, model context, or Server filesystem path.
type GenerationWorkflowClient struct {
	server *Client
}

func NewGenerationWorkflowClient(serverURL, apiKey string) (*GenerationWorkflowClient, error) {
	server, err := New(serverURL, apiKey)
	if err != nil {
		return nil, err
	}
	return &GenerationWorkflowClient{server: server}, nil
}

func (c *GenerationWorkflowClient) Claim(ctx context.Context, workerID string, leaseTTL time.Duration) (*domain.RuntimeActionContext, error) {
	var response struct {
		Action *domain.RuntimeActionContext `json:"action,omitempty"`
	}
	if err := c.post(ctx, "/api/internal/runtime-actions/claim", struct {
		WorkerID       string `json:"worker_id"`
		LeaseTTLMillis int64  `json:"lease_ttl_millis"`
	}{WorkerID: workerID, LeaseTTLMillis: leaseTTL.Milliseconds()}, &response); err != nil {
		return nil, err
	}
	if response.Action != nil {
		if err := response.Action.Valid(); err != nil {
			return nil, fmt.Errorf("server returned an invalid runtime action: %w", err)
		}
	}
	return response.Action, nil
}

func (c *GenerationWorkflowClient) Renew(ctx context.Context, credential domain.RuntimeActionCredential, leaseTTL time.Duration) error {
	if !credential.Valid() {
		return errors.New("runtime action renewal requires valid credentials")
	}
	return c.post(ctx, runtimeActionPath(credential.Identity.WorkflowID, "renew"), struct {
		domain.RuntimeActionCredential
		LeaseTTLMillis int64 `json:"lease_ttl_millis"`
	}{RuntimeActionCredential: credential, LeaseTTLMillis: leaseTTL.Milliseconds()}, nil)
}

func (c *GenerationWorkflowClient) CandidateArchive(ctx context.Context, credential domain.RuntimeActionCredential) ([]byte, string, error) {
	if !credential.Valid() || credential.Identity.State != domain.StateBuilding {
		return nil, "", errors.New("candidate archive requires a valid build action")
	}
	var response struct {
		Archive []byte `json:"archive"`
		SHA256  string `json:"sha256"`
	}
	if err := c.postLong(ctx, runtimeActionPath(credential.Identity.WorkflowID, "candidate/archive"), credential, &response); err != nil {
		return nil, "", err
	}
	return response.Archive, response.SHA256, nil
}

func (c *GenerationWorkflowClient) CompleteBuild(ctx context.Context, credential domain.RuntimeActionCredential, output domain.BuildOutput) error {
	return c.complete(ctx, credential, "build/complete", struct {
		domain.RuntimeActionCredential
		Output domain.BuildOutput `json:"output"`
	}{RuntimeActionCredential: credential, Output: output})
}

func (c *GenerationWorkflowClient) CompleteArtifactPublish(ctx context.Context, credential domain.RuntimeActionCredential, artifact domain.ArtifactReference) error {
	return c.complete(ctx, credential, "artifact-publish/complete", struct {
		domain.RuntimeActionCredential
		Artifact domain.ArtifactReference `json:"artifact"`
	}{RuntimeActionCredential: credential, Artifact: artifact})
}

func (c *GenerationWorkflowClient) RecordVerificationEnvironment(ctx context.Context, credential domain.RuntimeActionCredential, environment domain.VerificationEnvironment) error {
	return c.complete(ctx, credential, "verification/environment", struct {
		domain.RuntimeActionCredential
		Environment domain.VerificationEnvironment `json:"environment"`
	}{RuntimeActionCredential: credential, Environment: environment})
}

func (c *GenerationWorkflowClient) CompleteVerification(ctx context.Context, credential domain.RuntimeActionCredential, report domain.VerificationReport) error {
	return c.complete(ctx, credential, "verification/complete", struct {
		domain.RuntimeActionCredential
		Report domain.VerificationReport `json:"report"`
	}{RuntimeActionCredential: credential, Report: report})
}

func (c *GenerationWorkflowClient) RecordChallengePublication(ctx context.Context, credential domain.RuntimeActionCredential, artifact domain.ArtifactReference) error {
	return c.complete(ctx, credential, "challenge-publish/complete", struct {
		domain.RuntimeActionCredential
		Artifact domain.ArtifactReference `json:"artifact"`
	}{RuntimeActionCredential: credential, Artifact: artifact})
}

func (c *GenerationWorkflowClient) ReportInfrastructureFailure(ctx context.Context, credential domain.RuntimeActionCredential, failure domain.Failure) error {
	return c.complete(ctx, credential, "failure/infrastructure", struct {
		domain.RuntimeActionCredential
		Failure domain.Failure `json:"failure"`
	}{RuntimeActionCredential: credential, Failure: failure})
}

func (c *GenerationWorkflowClient) ReportArtifactFailure(ctx context.Context, credential domain.RuntimeActionCredential, failure domain.Failure, report *domain.VerificationReport) error {
	return c.complete(ctx, credential, "failure/artifact", struct {
		domain.RuntimeActionCredential
		Failure domain.Failure             `json:"failure"`
		Report  *domain.VerificationReport `json:"report,omitempty"`
	}{RuntimeActionCredential: credential, Failure: failure, Report: report})
}

func (c *GenerationWorkflowClient) complete(ctx context.Context, credential domain.RuntimeActionCredential, suffix string, body any) error {
	if !credential.Valid() {
		return errors.New("runtime action completion requires valid credentials")
	}
	return c.postLong(ctx, runtimeActionPath(credential.Identity.WorkflowID, suffix), body, nil)
}

func (c *GenerationWorkflowClient) ClaimResourceReap(ctx context.Context, workerID string, kind domain.ResourceReapKind, leaseTTL time.Duration) (*domain.ResourceReapClaim, error) {
	if strings.TrimSpace(workerID) == "" || !kind.Valid() || kind.Owner() != "runtime-worker" || leaseTTL <= 0 {
		return nil, errors.New("runtime resource reap claim is invalid")
	}
	var response struct {
		Claim *domain.ResourceReapClaim `json:"claim,omitempty"`
	}
	if err := c.post(ctx, "/api/internal/runtime-resource-reaps/claim", struct {
		WorkerID       string                  `json:"worker_id"`
		Kind           domain.ResourceReapKind `json:"kind"`
		LeaseTTLMillis int64                   `json:"lease_ttl_millis"`
	}{WorkerID: workerID, Kind: kind, LeaseTTLMillis: leaseTTL.Milliseconds()}, &response); err != nil {
		return nil, err
	}
	if response.Claim != nil && response.Claim.Valid() != nil {
		return nil, errors.New("server returned an invalid runtime resource reap claim")
	}
	return response.Claim, nil
}

func (c *GenerationWorkflowClient) CompleteResourceReap(ctx context.Context, claim domain.ResourceReapClaim, failure string) error {
	if claim.Valid() != nil {
		return errors.New("runtime resource reap completion is invalid")
	}
	return c.post(ctx, "/api/internal/runtime-resource-reaps/complete", struct {
		Claim   domain.ResourceReapClaim `json:"claim"`
		Failure string                   `json:"failure,omitempty"`
	}{Claim: claim, Failure: strings.TrimSpace(failure)}, nil)
}

func (c *GenerationWorkflowClient) post(ctx context.Context, path string, body, output any) error {
	if c == nil || c.server == nil {
		return errors.New("runtime worker client is not configured")
	}
	return mapRuntimeActionError(c.server.Post(ctx, path, body, output))
}

func (c *GenerationWorkflowClient) postLong(ctx context.Context, path string, body, output any) error {
	if c == nil || c.server == nil {
		return errors.New("runtime worker client is not configured")
	}
	return mapRuntimeActionError(c.server.PostLong(ctx, path, body, output))
}

func runtimeActionPath(workflowID, suffix string) string {
	return "/api/internal/runtime-actions/" + url.PathEscape(strings.TrimSpace(workflowID)) + "/" + suffix
}

func mapRuntimeActionError(err error) error {
	if err == nil {
		return nil
	}
	if IsStatus(err, http.StatusConflict) {
		return domain.ErrLeaseLost
	}
	if IsStatus(err, http.StatusNotFound) {
		return domain.ErrWorkflowNotFound
	}
	return fmt.Errorf("runtime worker server request: %w", err)
}
