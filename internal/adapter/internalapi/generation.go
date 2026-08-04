package internalapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	app "github.com/breakfix/breakfix/internal/application/generation"
	roadmapapp "github.com/breakfix/breakfix/internal/application/roadmap"
	domain "github.com/breakfix/breakfix/internal/domain/generation"
	roadmapdomain "github.com/breakfix/breakfix/internal/domain/roadmap"
)

// GenerationWorkflowClient is the Generate Worker's complete Server boundary. It intentionally
// exposes one workflow lease rather than the former per-stage task queue.
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

func (c *GenerationWorkflowClient) Claim(ctx context.Context, workerID string, leaseTTL time.Duration) (*domain.Claim, error) {
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

func (c *GenerationWorkflowClient) Renew(ctx context.Context, claim domain.Claim, leaseTTL time.Duration) error {
	if !claim.Valid() {
		return errors.New("renew generation workflow requires a valid claim")
	}
	return c.post(ctx, generationWorkflowPath(claim.Workflow.ID, "renew"), struct {
		domain.LeaseCredential
		LeaseTTLMillis int64 `json:"lease_ttl_millis"`
	}{LeaseCredential: claim.LeaseCredential, LeaseTTLMillis: leaseTTL.Milliseconds()}, nil)
}

func (c *GenerationWorkflowClient) Context(ctx context.Context, claim domain.Claim) (*domain.Context, error) {
	if !claim.Valid() {
		return nil, errors.New("generation workflow context requires a valid claim")
	}
	var response struct {
		Context domain.Context `json:"context"`
	}
	if err := c.postLong(ctx, generationWorkflowPath(claim.Workflow.ID, "context"), claim.LeaseCredential, &response); err != nil {
		return nil, err
	}
	if response.Context.Workflow.ID != claim.Workflow.ID || response.Context.Workflow.State != claim.Workflow.State {
		return nil, errors.New("server returned generation context for a different workflow state")
	}
	return &response.Context, nil
}

func (c *GenerationWorkflowClient) StartAgentRun(ctx context.Context, claim domain.Claim, request app.StartAgentRunRequest) (*app.StartAgentRunResponse, error) {
	if !claim.Valid() {
		return nil, errors.New("start generation agent run requires a valid claim")
	}
	request.LeaseCredential = claim.LeaseCredential
	request.ExpectedState = claim.Workflow.State
	if err := request.Validate(claim.Workflow.ID); err != nil {
		return nil, err
	}
	var response app.StartAgentRunResponse
	if err := c.post(ctx, generationWorkflowPath(claim.Workflow.ID, "agent-runs"), request, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

// Phase atomically records one typed stage result and returns the refreshed
// lease view. A failure report that releases the lease returns a nil claim.
func (c *GenerationWorkflowClient) Phase(ctx context.Context, claim domain.Claim, request app.PhaseRequest) (*domain.Claim, error) {
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
	if err := c.postLong(ctx, generationWorkflowPath(claim.Workflow.ID, "phase"), request, &response); err != nil {
		return nil, err
	}
	if response.Claim != nil && !response.Claim.Valid() {
		return nil, errors.New("server returned an invalid generation phase claim")
	}
	return response.Claim, nil
}

func (c *GenerationWorkflowClient) CandidateArchive(ctx context.Context, claim domain.Claim) ([]byte, string, error) {
	var response struct {
		Archive []byte `json:"archive"`
		SHA256  string `json:"sha256"`
	}
	if err := c.postLong(ctx, generationWorkflowPath(claim.Workflow.ID, "candidate/archive"), claim.LeaseCredential, &response); err != nil {
		return nil, "", err
	}
	return response.Archive, response.SHA256, nil
}

func (c *GenerationWorkflowClient) K8sBase(ctx context.Context, claim domain.Claim) ([]byte, string, error) {
	var response struct {
		Archive []byte `json:"archive"`
		SHA256  string `json:"sha256"`
	}
	if err := c.postLong(ctx, generationWorkflowPath(claim.Workflow.ID, "k8s/base"), claim.LeaseCredential, &response); err != nil {
		return nil, "", err
	}
	return response.Archive, response.SHA256, nil
}

func (c *GenerationWorkflowClient) BuildArchive(ctx context.Context, claim domain.Claim) ([]byte, string, error) {
	var response struct {
		Archive []byte `json:"archive"`
		SHA256  string `json:"sha256"`
	}
	if err := c.postLong(ctx, generationWorkflowPath(claim.Workflow.ID, "build/archive"), claim.LeaseCredential, &response); err != nil {
		return nil, "", err
	}
	return response.Archive, response.SHA256, nil
}

// SearchClassificationTopics reads only the revision pinned on the claimed
// workflow. The Worker never supplies a revision identifier to the Server.
func (c *GenerationWorkflowClient) SearchClassificationTopics(ctx context.Context, claim domain.Claim, query roadmapapp.TopicSearch) ([]roadmapapp.TopicMatch, error) {
	if !claim.Valid() || query.Validate() != nil {
		return nil, errors.New("classification topic search requires a valid claim and query")
	}
	var response struct {
		RoadmapRevision string                  `json:"roadmap_revision"`
		Topics          []roadmapapp.TopicMatch `json:"topics"`
	}
	if err := c.post(ctx, generationWorkflowPath(claim.Workflow.ID, "classification/topics/search"), struct {
		domain.LeaseCredential
		Query    string `json:"query"`
		DomainID string `json:"domain_id,omitempty"`
		Limit    int    `json:"limit"`
	}{LeaseCredential: claim.LeaseCredential, Query: query.Query, DomainID: query.DomainID, Limit: query.Limit}, &response); err != nil {
		return nil, err
	}
	if response.RoadmapRevision != claim.Workflow.ClassificationRoadmapRevision {
		return nil, errors.New("Server returned a classification topic search from a different roadmap revision")
	}
	return response.Topics, nil
}

func (c *GenerationWorkflowClient) ReadClassificationTopic(ctx context.Context, claim domain.Claim, id string) (*roadmapdomain.Topic, error) {
	if !claim.Valid() || strings.TrimSpace(id) == "" {
		return nil, errors.New("classification topic read requires a valid claim and id")
	}
	var response struct {
		RoadmapRevision string              `json:"roadmap_revision"`
		Topic           roadmapdomain.Topic `json:"topic"`
	}
	if err := c.post(ctx, generationWorkflowPath(claim.Workflow.ID, "classification/topics/read"), struct {
		domain.LeaseCredential
		ID string `json:"id"`
	}{LeaseCredential: claim.LeaseCredential, ID: strings.TrimSpace(id)}, &response); err != nil {
		return nil, err
	}
	if response.RoadmapRevision != claim.Workflow.ClassificationRoadmapRevision {
		return nil, errors.New("Server returned a classification topic from a different roadmap revision")
	}
	return &response.Topic, nil
}

func (c *GenerationWorkflowClient) SearchClassificationTags(ctx context.Context, claim domain.Claim, query roadmapapp.TagSearch) ([]roadmapapp.TagMatch, error) {
	if !claim.Valid() || query.Validate() != nil {
		return nil, errors.New("classification tag search requires a valid claim and query")
	}
	var response struct {
		RoadmapRevision string                `json:"roadmap_revision"`
		Tags            []roadmapapp.TagMatch `json:"tags"`
	}
	if err := c.post(ctx, generationWorkflowPath(claim.Workflow.ID, "classification/tags/search"), struct {
		domain.LeaseCredential
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}{LeaseCredential: claim.LeaseCredential, Query: query.Query, Limit: query.Limit}, &response); err != nil {
		return nil, err
	}
	if response.RoadmapRevision != claim.Workflow.ClassificationRoadmapRevision {
		return nil, errors.New("Server returned a classification tag search from a different roadmap revision")
	}
	return response.Tags, nil
}

func (c *GenerationWorkflowClient) ReadClassificationTag(ctx context.Context, claim domain.Claim, id string) (*roadmapdomain.Tag, error) {
	if !claim.Valid() || strings.TrimSpace(id) == "" {
		return nil, errors.New("classification tag read requires a valid claim and id")
	}
	var response struct {
		RoadmapRevision string            `json:"roadmap_revision"`
		Tag             roadmapdomain.Tag `json:"tag"`
	}
	if err := c.post(ctx, generationWorkflowPath(claim.Workflow.ID, "classification/tags/read"), struct {
		domain.LeaseCredential
		ID string `json:"id"`
	}{LeaseCredential: claim.LeaseCredential, ID: strings.TrimSpace(id)}, &response); err != nil {
		return nil, err
	}
	if response.RoadmapRevision != claim.Workflow.ClassificationRoadmapRevision {
		return nil, errors.New("Server returned a classification tag from a different roadmap revision")
	}
	return &response.Tag, nil
}

func (c *GenerationWorkflowClient) ClaimResourceReap(ctx context.Context, workerID string, kind domain.ResourceReapKind, leaseTTL time.Duration) (*domain.ResourceReapClaim, error) {
	if strings.TrimSpace(workerID) == "" || !kind.Valid() || kind.Owner() != "generate-worker" || leaseTTL <= 0 {
		return nil, errors.New("generation resource reap claim is invalid")
	}
	var response struct {
		Claim *domain.ResourceReapClaim `json:"claim,omitempty"`
	}
	if err := c.post(ctx, "/api/internal/generation-resource-reaps/claim", struct {
		WorkerID       string                  `json:"worker_id"`
		Kind           domain.ResourceReapKind `json:"kind"`
		LeaseTTLMillis int64                   `json:"lease_ttl_millis"`
	}{WorkerID: workerID, Kind: kind, LeaseTTLMillis: leaseTTL.Milliseconds()}, &response); err != nil {
		return nil, err
	}
	if response.Claim != nil && response.Claim.Valid() != nil {
		return nil, errors.New("server returned an invalid generation resource reap claim")
	}
	return response.Claim, nil
}

func (c *GenerationWorkflowClient) CompleteResourceReap(ctx context.Context, claim domain.ResourceReapClaim, failure string) error {
	if claim.Valid() != nil {
		return errors.New("generation resource reap completion is invalid")
	}
	return c.post(ctx, "/api/internal/generation-resource-reaps/complete", struct {
		Claim   domain.ResourceReapClaim `json:"claim"`
		Failure string                   `json:"failure,omitempty"`
	}{Claim: claim, Failure: strings.TrimSpace(failure)}, nil)
}

func (c *GenerationWorkflowClient) post(ctx context.Context, path string, body, output any) error {
	if c == nil || c.server == nil {
		return errors.New("generation worker client is not configured")
	}
	return mapGenerationWorkflowError(c.server.Post(ctx, path, body, output))
}

func (c *GenerationWorkflowClient) postLong(ctx context.Context, path string, body, output any) error {
	if c == nil || c.server == nil {
		return errors.New("generation worker client is not configured")
	}
	return mapGenerationWorkflowError(c.server.PostLong(ctx, path, body, output))
}

func generationWorkflowPath(id, suffix string) string {
	return "/api/internal/generation-workflows/" + url.PathEscape(strings.TrimSpace(id)) + "/" + suffix
}

func mapGenerationWorkflowError(err error) error {
	if err == nil {
		return nil
	}
	if IsStatus(err, http.StatusConflict) {
		return domain.ErrLeaseLost
	}
	if IsStatus(err, http.StatusNotFound) {
		return domain.ErrWorkflowNotFound
	}
	return fmt.Errorf("generation worker server request: %w", err)
}
