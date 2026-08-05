package internalapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/domain/execution"
	runtime "github.com/breakfix/breakfix/internal/domain/runtime"
)

// RuntimeActionClient is the Runtime Worker's complete Server boundary.
// It only exposes one lease-fenced RuntimeAction at a time; it cannot read an
// authoring Plan, workspace, model context, or Server filesystem path.
type RuntimeActionClient struct {
	server *Client
}

func NewRuntimeActionClient(serverURL, apiKey string) (*RuntimeActionClient, error) {
	server, err := New(serverURL, apiKey)
	if err != nil {
		return nil, err
	}
	return &RuntimeActionClient{server: server}, nil
}

func (c *RuntimeActionClient) Claim(ctx context.Context, workerID string, leaseTTL time.Duration) (*runtime.Context, error) {
	var response struct {
		Action *runtime.Context `json:"action,omitempty"`
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

func (c *RuntimeActionClient) Renew(ctx context.Context, credential runtime.Credential, leaseTTL time.Duration) error {
	if !credential.Valid() {
		return errors.New("runtime action renewal requires valid credentials")
	}
	return c.post(ctx, runtimeActionPath(credential.Identity.OwnerID, "renew"), struct {
		runtime.Credential
		LeaseTTLMillis int64 `json:"lease_ttl_millis"`
	}{Credential: credential, LeaseTTLMillis: leaseTTL.Milliseconds()}, nil)
}

func (c *RuntimeActionClient) Archive(ctx context.Context, credential runtime.Credential) ([]byte, string, error) {
	if !credential.Valid() || credential.Identity.State != runtime.StateBuilding {
		return nil, "", errors.New("candidate archive requires a valid build action")
	}
	var response struct {
		Archive []byte `json:"archive"`
		SHA256  string `json:"sha256"`
	}
	if err := c.postLong(ctx, runtimeActionPath(credential.Identity.OwnerID, "source/archive"), credential, &response); err != nil {
		return nil, "", err
	}
	return response.Archive, response.SHA256, nil
}

func (c *RuntimeActionClient) CompleteBuild(ctx context.Context, credential runtime.Credential, output execution.BuildOutput) error {
	return c.complete(ctx, credential, "build/complete", struct {
		runtime.Credential
		Output execution.BuildOutput `json:"output"`
	}{Credential: credential, Output: output})
}

func (c *RuntimeActionClient) CompleteArtifactPublish(ctx context.Context, credential runtime.Credential, artifact execution.ArtifactReference) error {
	return c.complete(ctx, credential, "artifact-publish/complete", struct {
		runtime.Credential
		Artifact execution.ArtifactReference `json:"artifact"`
	}{Credential: credential, Artifact: artifact})
}

func (c *RuntimeActionClient) RecordVerificationEnvironment(ctx context.Context, credential runtime.Credential, environment execution.VerificationEnvironment) error {
	return c.complete(ctx, credential, "verification/environment", struct {
		runtime.Credential
		Environment execution.VerificationEnvironment `json:"environment"`
	}{Credential: credential, Environment: environment})
}

func (c *RuntimeActionClient) CompleteVerification(ctx context.Context, credential runtime.Credential, report execution.VerificationReport) error {
	return c.complete(ctx, credential, "verification/complete", struct {
		runtime.Credential
		Report execution.VerificationReport `json:"report"`
	}{Credential: credential, Report: report})
}

func (c *RuntimeActionClient) RecordChallengePublication(ctx context.Context, credential runtime.Credential, artifact execution.ArtifactReference) error {
	return c.complete(ctx, credential, "challenge-publish/complete", struct {
		runtime.Credential
		Artifact execution.ArtifactReference `json:"artifact"`
	}{Credential: credential, Artifact: artifact})
}

func (c *RuntimeActionClient) ReportInfrastructureFailure(ctx context.Context, credential runtime.Credential, failure runtime.Failure) error {
	return c.complete(ctx, credential, "failure/infrastructure", struct {
		runtime.Credential
		Failure runtime.Failure `json:"failure"`
	}{Credential: credential, Failure: failure})
}

func (c *RuntimeActionClient) ReportArtifactFailure(ctx context.Context, credential runtime.Credential, failure runtime.Failure, report *execution.VerificationReport) error {
	return c.complete(ctx, credential, "failure/artifact", struct {
		runtime.Credential
		Failure runtime.Failure               `json:"failure"`
		Report  *execution.VerificationReport `json:"report,omitempty"`
	}{Credential: credential, Failure: failure, Report: report})
}

func (c *RuntimeActionClient) complete(ctx context.Context, credential runtime.Credential, suffix string, body any) error {
	if !credential.Valid() {
		return errors.New("runtime action completion requires valid credentials")
	}
	return c.postLong(ctx, runtimeActionPath(credential.Identity.OwnerID, suffix), body, nil)
}

func (c *RuntimeActionClient) ClaimResourceReap(ctx context.Context, workerID string, leaseTTL time.Duration) (*runtime.ReapClaim, error) {
	if strings.TrimSpace(workerID) == "" || leaseTTL <= 0 {
		return nil, errors.New("runtime resource reap claim is invalid")
	}
	var response struct {
		Claim *runtime.ReapClaim `json:"claim,omitempty"`
	}
	if err := c.post(ctx, "/api/internal/runtime-resource-reaps/claim", struct {
		WorkerID       string `json:"worker_id"`
		LeaseTTLMillis int64  `json:"lease_ttl_millis"`
	}{WorkerID: workerID, LeaseTTLMillis: leaseTTL.Milliseconds()}, &response); err != nil {
		return nil, err
	}
	if response.Claim != nil && response.Claim.Valid() != nil {
		return nil, errors.New("server returned an invalid runtime resource reap claim")
	}
	return response.Claim, nil
}

func (c *RuntimeActionClient) CompleteResourceReap(ctx context.Context, claim runtime.ReapClaim, failure string) error {
	if claim.Valid() != nil {
		return errors.New("runtime resource reap completion is invalid")
	}
	return c.post(ctx, "/api/internal/runtime-resource-reaps/complete", struct {
		Claim   runtime.ReapClaim `json:"claim"`
		Failure string            `json:"failure,omitempty"`
	}{Claim: claim, Failure: strings.TrimSpace(failure)}, nil)
}

func (c *RuntimeActionClient) post(ctx context.Context, path string, body, output any) error {
	if c == nil || c.server == nil {
		return errors.New("runtime worker client is not configured")
	}
	return mapRuntimeActionError(c.server.Post(ctx, path, body, output))
}

func (c *RuntimeActionClient) postLong(ctx context.Context, path string, body, output any) error {
	if c == nil || c.server == nil {
		return errors.New("runtime worker client is not configured")
	}
	return mapRuntimeActionError(c.server.PostLong(ctx, path, body, output))
}

func runtimeActionPath(ownerID, suffix string) string {
	return "/api/internal/runtime-actions/" + url.PathEscape(strings.TrimSpace(ownerID)) + "/" + suffix
}

func mapRuntimeActionError(err error) error {
	if err == nil {
		return nil
	}
	if IsStatus(err, http.StatusConflict) {
		return runtime.ErrLeaseLost
	}
	if IsStatus(err, http.StatusNotFound) {
		return runtime.ErrActionNotFound
	}
	return fmt.Errorf("runtime worker server request: %w", err)
}
