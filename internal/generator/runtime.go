package generator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/breakfix/breakfix/internal/adapter/internalapi"
	"github.com/breakfix/breakfix/internal/domain/generation"
)

// RuntimeClient is the narrow Server proxy used by the Generator's sandbox
// tools. Every request is fenced by the enclosing GenerationWorkflow lease.
type RuntimeClient interface {
	LoadWorkspace(context.Context, generation.Claim) (WorkspaceContext, error)
	ReadFile(context.Context, generation.Claim, string, int, int) (FileReadResponse, error)
	WriteFile(context.Context, generation.Claim, string, string) error
	Execute(context.Context, generation.Claim, string, func(ExecuteEvent) error) error
	ArchiveWorkspace(context.Context, generation.Claim) (ArchiveResponse, error)
}

type InternalClient struct{ server *internalapi.Client }

func NewInternalClient(serverURL, apiKey string) (*InternalClient, error) {
	client, err := internalapi.New(serverURL, apiKey)
	if err != nil {
		return nil, err
	}
	return &InternalClient{server: client}, nil
}

func (c *InternalClient) LoadWorkspace(ctx context.Context, claim generation.Claim) (WorkspaceContext, error) {
	var result WorkspaceContext
	err := c.postLong(ctx, claim, "generator/context", claim.LeaseCredential, &result)
	return result, err
}

func (c *InternalClient) ReadFile(ctx context.Context, claim generation.Claim, path string, offset, limit int) (FileReadResponse, error) {
	var result FileReadResponse
	err := c.post(ctx, claim, "generator/files/read", struct {
		generation.LeaseCredential
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}{LeaseCredential: claim.LeaseCredential, Path: path, Offset: offset, Limit: limit}, &result)
	return result, err
}

func (c *InternalClient) WriteFile(ctx context.Context, claim generation.Claim, path, content string) error {
	return c.post(ctx, claim, "generator/files/write", struct {
		generation.LeaseCredential
		Path    string `json:"path"`
		Content string `json:"content"`
	}{LeaseCredential: claim.LeaseCredential, Path: path, Content: content}, nil)
}

func (c *InternalClient) Execute(ctx context.Context, claim generation.Claim, command string, consume func(ExecuteEvent) error) error {
	if c == nil || c.server == nil {
		return errors.New("generator internal client is not configured")
	}
	err := c.server.PostStream(ctx, workflowPath(claim.Workflow.ID, "generator/execute"), struct {
		generation.LeaseCredential
		Command string `json:"command"`
	}{LeaseCredential: claim.LeaseCredential, Command: command}, func(raw json.RawMessage) error {
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
	return mapClientError(err)
}

func (c *InternalClient) ArchiveWorkspace(ctx context.Context, claim generation.Claim) (ArchiveResponse, error) {
	var result ArchiveResponse
	err := c.postLong(ctx, claim, "generator/archive", claim.LeaseCredential, &result)
	return result, err
}

func (c *InternalClient) post(ctx context.Context, claim generation.Claim, suffix string, body, output any) error {
	if c == nil || c.server == nil || !claim.Valid() {
		return errors.New("generator internal client requires a valid workflow claim")
	}
	return mapClientError(c.server.Post(ctx, workflowPath(claim.Workflow.ID, suffix), body, output))
}

func (c *InternalClient) postLong(ctx context.Context, claim generation.Claim, suffix string, body, output any) error {
	if c == nil || c.server == nil || !claim.Valid() {
		return errors.New("generator internal client requires a valid workflow claim")
	}
	return mapClientError(c.server.PostLong(ctx, workflowPath(claim.Workflow.ID, suffix), body, output))
}

func workflowPath(id, suffix string) string {
	return "/api/internal/generation-workflows/" + url.PathEscape(strings.TrimSpace(id)) + "/" + suffix
}

func mapClientError(err error) error {
	if err == nil {
		return nil
	}
	if internalapi.IsStatus(err, http.StatusConflict) {
		return generation.ErrLeaseLost
	}
	if internalapi.IsStatus(err, http.StatusNotFound) {
		return generation.ErrWorkflowNotFound
	}
	return fmt.Errorf("generator server request: %w", err)
}
