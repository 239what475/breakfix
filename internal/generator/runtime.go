package generator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/breakfix/breakfix/internal/agentruntime"
	"github.com/breakfix/breakfix/internal/agentserver"
	"github.com/breakfix/breakfix/internal/authoring"
)

type LeaseCredential = agentruntime.LeaseCredential

type WorkspaceContext struct {
	Plan     authoring.Plan `json:"plan"`
	Feedback Feedback       `json:"feedback"`
}

type FileReadResponse struct {
	Content string `json:"content"`
}

type ArchiveResponse struct {
	Archive []byte `json:"archive"`
}

type FinalizedCandidate struct {
	CandidateRevisionID string `json:"candidate_revision_id"`
}

type ExecuteEvent struct {
	Type     string `json:"type"`
	Content  string `json:"content,omitempty"`
	ExitCode *int   `json:"exit_code,omitempty"`
	Error    string `json:"error,omitempty"`
}

type RuntimeClient interface {
	LoadWorkspace(context.Context, agentruntime.Claim) (WorkspaceContext, error)
	ReadFile(context.Context, agentruntime.Claim, string, int, int) (FileReadResponse, error)
	WriteFile(context.Context, agentruntime.Claim, string, string) error
	Execute(context.Context, agentruntime.Claim, string, func(ExecuteEvent) error) error
	ArchiveWorkspace(context.Context, agentruntime.Claim) (ArchiveResponse, error)
	FinalizeCandidate(context.Context, agentruntime.Claim, []byte) (FinalizedCandidate, error)
}

type InternalClient struct{ server *agentserver.Client }

func NewInternalClient(serverURL, apiKey string) (*InternalClient, error) {
	client, err := agentserver.New(serverURL, apiKey)
	if err != nil {
		return nil, err
	}
	return &InternalClient{server: client}, nil
}

func (c *InternalClient) LoadWorkspace(ctx context.Context, claim agentruntime.Claim) (WorkspaceContext, error) {
	var result WorkspaceContext
	err := c.postLong(ctx, claim.Run.ID, "/generator/context", claim.Credential(), &result)
	return result, err
}

func (c *InternalClient) ReadFile(ctx context.Context, claim agentruntime.Claim, path string, offset, limit int) (FileReadResponse, error) {
	var result FileReadResponse
	err := c.post(ctx, claim.Run.ID, "/generator/files/read", struct {
		LeaseCredential
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}{LeaseCredential: claim.Credential(), Path: path, Offset: offset, Limit: limit}, &result)
	return result, err
}

func (c *InternalClient) WriteFile(ctx context.Context, claim agentruntime.Claim, path, content string) error {
	return c.post(ctx, claim.Run.ID, "/generator/files/write", struct {
		LeaseCredential
		Path    string `json:"path"`
		Content string `json:"content"`
	}{LeaseCredential: claim.Credential(), Path: path, Content: content}, nil)
}

func (c *InternalClient) Execute(ctx context.Context, claim agentruntime.Claim, command string, consume func(ExecuteEvent) error) error {
	if c == nil || c.server == nil {
		return errors.New("generator internal client is not configured")
	}
	return c.server.PostStream(ctx, "/api/internal/agent-runs/"+url.PathEscape(claim.Run.ID)+"/generator/execute", struct {
		LeaseCredential
		Command string `json:"command"`
	}{LeaseCredential: claim.Credential(), Command: command}, func(raw json.RawMessage) error {
		var event ExecuteEvent
		decoder := json.NewDecoder(strings.NewReader(string(raw)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&event); err != nil {
			return fmt.Errorf("decode generator execute event: %w", err)
		}
		if event.Type != "stdout" && event.Type != "result" && event.Type != "error" {
			return fmt.Errorf("unsupported generator execute event %q", event.Type)
		}
		if event.Type == "error" && strings.TrimSpace(event.Error) == "" {
			return errors.New("generator execute error event has no message")
		}
		return consume(event)
	})
}

func (c *InternalClient) ArchiveWorkspace(ctx context.Context, claim agentruntime.Claim) (ArchiveResponse, error) {
	var result ArchiveResponse
	err := c.postLong(ctx, claim.Run.ID, "/generator/archive", claim.Credential(), &result)
	return result, err
}

func (c *InternalClient) FinalizeCandidate(ctx context.Context, claim agentruntime.Claim, archive []byte) (FinalizedCandidate, error) {
	var result FinalizedCandidate
	err := c.postLong(ctx, claim.Run.ID, "/generator/finalize", struct {
		LeaseCredential
		Archive []byte `json:"archive"`
	}{LeaseCredential: claim.Credential(), Archive: archive}, &result)
	if err == nil && strings.TrimSpace(result.CandidateRevisionID) == "" {
		return FinalizedCandidate{}, errors.New("generator finalization returned no candidate revision")
	}
	return result, err
}

func (c *InternalClient) post(ctx context.Context, runID, suffix string, body any, output any) error {
	if c == nil || c.server == nil {
		return errors.New("generator internal client is not configured")
	}
	return c.server.Post(ctx, "/api/internal/agent-runs/"+url.PathEscape(runID)+suffix, body, output)
}

func (c *InternalClient) postLong(ctx context.Context, runID, suffix string, body any, output any) error {
	if c == nil || c.server == nil {
		return errors.New("generator internal client is not configured")
	}
	return c.server.PostLong(ctx, "/api/internal/agent-runs/"+url.PathEscape(runID)+suffix, body, output)
}
