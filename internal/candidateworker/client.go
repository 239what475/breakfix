// Package candidateworker implements the common fixed-worker control loop and
// the authenticated Server handoff protocol for candidate pipeline stages.
package candidateworker

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/breakfix/breakfix/internal/agentserver"
	"github.com/breakfix/breakfix/internal/candidate"
	"github.com/breakfix/breakfix/internal/worklist"
)

type Claim struct {
	Work      worklist.Claim          `json:"work"`
	Candidate candidate.WorkerView    `json:"candidate"`
	Cleanup   *candidate.CleanupHints `json:"cleanup,omitempty"`
}

func (c Claim) Valid() bool {
	if !c.Work.Valid() || c.Work.Item.SubjectID != c.Candidate.ID {
		return false
	}
	if c.Work.Item.Kind == worklist.KindArtifactCleanup {
		return c.Cleanup != nil && c.Cleanup.Validate() == nil
	}
	return c.Cleanup == nil
}

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

func (c *Client) Claim(ctx context.Context, kind worklist.Kind, workerID string, leaseTTL time.Duration) (*Claim, error) {
	var response struct {
		Claim *Claim `json:"claim,omitempty"`
	}
	err := c.server.Post(ctx, candidateWorkPath(kind, "claim"), struct {
		WorkerID       string `json:"worker_id"`
		LeaseTTLMillis int64  `json:"lease_ttl_millis"`
	}{WorkerID: workerID, LeaseTTLMillis: leaseTTL.Milliseconds()}, &response)
	if err != nil {
		return nil, mapServerError(err)
	}
	if response.Claim != nil && !response.Claim.Valid() {
		return nil, errors.New("server returned an invalid candidate work claim")
	}
	return response.Claim, nil
}

func (c *Client) Renew(ctx context.Context, claim Claim, leaseTTL time.Duration) error {
	return c.post(ctx, claim, "renew", struct {
		worklist.Credential
		LeaseTTLMillis int64 `json:"lease_ttl_millis"`
	}{Credential: claim.Work.Credential(), LeaseTTLMillis: leaseTTL.Milliseconds()}, nil)
}

func (c *Client) Requeue(ctx context.Context, claim Claim, next time.Time, code, summary string) error {
	return c.post(ctx, claim, "requeue", struct {
		worklist.Credential
		NextRunAt time.Time `json:"next_run_at"`
		Code      string    `json:"code"`
		Summary   string    `json:"summary"`
	}{Credential: claim.Work.Credential(), NextRunAt: next.UTC(), Code: code, Summary: summary}, nil)
}

func (c *Client) DownloadCandidateArchive(ctx context.Context, claim Claim) ([]byte, string, error) {
	var response struct {
		Archive []byte `json:"archive"`
		SHA256  string `json:"sha256"`
	}
	err := c.postLong(ctx, claim, "candidate/archive", claim.Work.Credential(), &response)
	return response.Archive, response.SHA256, err
}

func (c *Client) DownloadK8sBase(ctx context.Context, claim Claim) ([]byte, string, error) {
	var response struct {
		Archive []byte `json:"archive"`
		SHA256  string `json:"sha256"`
	}
	err := c.postLong(ctx, claim, "k8s/base", claim.Work.Credential(), &response)
	return response.Archive, response.SHA256, err
}

func (c *Client) CompleteBuild(ctx context.Context, claim Claim, output candidate.BuildOutput, archive []byte) error {
	return c.postLong(ctx, claim, "complete/build", struct {
		worklist.Credential
		Output  candidate.BuildOutput `json:"output"`
		Archive []byte                `json:"archive,omitempty"`
	}{Credential: claim.Work.Credential(), Output: output, Archive: archive}, nil)
}

func (c *Client) DownloadBuildArchive(ctx context.Context, claim Claim) ([]byte, string, error) {
	var response struct {
		Archive []byte `json:"archive"`
		SHA256  string `json:"sha256"`
	}
	err := c.postLong(ctx, claim, "build/archive", claim.Work.Credential(), &response)
	return response.Archive, response.SHA256, err
}

func (c *Client) CompleteArtifactPublish(ctx context.Context, claim Claim, artifact candidate.ArtifactReference) error {
	return c.post(ctx, claim, "complete/artifact-publish", struct {
		worklist.Credential
		Artifact candidate.ArtifactReference `json:"artifact"`
	}{Credential: claim.Work.Credential(), Artifact: artifact}, nil)
}

func (c *Client) RecordVerificationEnvironment(ctx context.Context, claim Claim, environment candidate.VerificationEnvironment) error {
	return c.post(ctx, claim, "verify/environment", struct {
		worklist.Credential
		Environment candidate.VerificationEnvironment `json:"environment"`
	}{Credential: claim.Work.Credential(), Environment: environment}, nil)
}

func (c *Client) CompleteVerification(ctx context.Context, claim Claim, report candidate.VerificationReport) error {
	return c.post(ctx, claim, "complete/verify", struct {
		worklist.Credential
		Report candidate.VerificationReport `json:"report"`
	}{Credential: claim.Work.Credential(), Report: report}, nil)
}

func (c *Client) FailArtifact(ctx context.Context, claim Claim, failure candidate.Failure, report *candidate.VerificationReport) error {
	return c.post(ctx, claim, "fail/artifact", struct {
		worklist.Credential
		Failure candidate.Failure             `json:"failure"`
		Report  *candidate.VerificationReport `json:"report,omitempty"`
	}{Credential: claim.Work.Credential(), Failure: failure, Report: report}, nil)
}

func (c *Client) CompleteCleanup(ctx context.Context, claim Claim) error {
	return c.post(ctx, claim, "complete/cleanup", claim.Work.Credential(), nil)
}

func (c *Client) CompleteChallengePublish(ctx context.Context, claim Claim, artifact candidate.ArtifactReference) error {
	return c.post(ctx, claim, "complete/challenge-publish", struct {
		worklist.Credential
		Artifact candidate.ArtifactReference `json:"artifact"`
	}{Credential: claim.Work.Credential(), Artifact: artifact}, nil)
}

func (c *Client) post(ctx context.Context, claim Claim, operation string, body, output any) error {
	if c == nil || c.server == nil || !claim.Valid() {
		return errors.New("candidate worker client requires a valid claim")
	}
	err := c.server.Post(ctx, candidateClaimPath(claim, operation), body, output)
	return mapServerError(err)
}

func (c *Client) postLong(ctx context.Context, claim Claim, operation string, body, output any) error {
	if c == nil || c.server == nil || !claim.Valid() {
		return errors.New("candidate worker client requires a valid claim")
	}
	err := c.server.PostLong(ctx, candidateClaimPath(claim, operation), body, output)
	return mapServerError(err)
}

func candidateWorkPath(kind worklist.Kind, operation string) string {
	return "/api/internal/work-items/" + url.PathEscape(string(kind)) + "/" + operation
}

func candidateClaimPath(claim Claim, operation string) string {
	return candidateWorkPath(claim.Work.Item.Kind, url.PathEscape(claim.Work.Item.ID)+"/"+operation)
}

func mapServerError(err error) error {
	if err == nil {
		return nil
	}
	if agentserver.IsStatus(err, http.StatusConflict) {
		return worklist.ErrLeaseLost
	}
	if agentserver.IsStatus(err, http.StatusNotFound) {
		return worklist.ErrNotFound
	}
	return fmt.Errorf("candidate worker server request: %w", err)
}
