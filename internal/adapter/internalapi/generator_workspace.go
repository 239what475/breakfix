package internalapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	app "github.com/breakfix/breakfix/internal/application/generation"
	"github.com/breakfix/breakfix/internal/domain/generation"
)

// GeneratorWorkspaceClient is the lease-fenced Server proxy used by the
// Generator agent's workspace tools.
type GeneratorWorkspaceClient struct{ server *Client }

func NewGeneratorWorkspaceClient(serverURL, apiKey string) (*GeneratorWorkspaceClient, error) {
	client, err := New(serverURL, apiKey)
	if err != nil {
		return nil, err
	}
	return &GeneratorWorkspaceClient{server: client}, nil
}

func (c *GeneratorWorkspaceClient) LoadWorkspace(ctx context.Context, claim generation.Claim) (app.WorkspaceContext, error) {
	var result app.WorkspaceContext
	err := c.postLong(ctx, claim, "generator/context", claim.LeaseCredential, &result)
	return result, err
}

func (c *GeneratorWorkspaceClient) ReadFile(ctx context.Context, claim generation.Claim, path string, offset, limit int) (app.FileReadResponse, error) {
	var result app.FileReadResponse
	err := c.post(ctx, claim, "generator/files/read", struct {
		generation.LeaseCredential
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}{LeaseCredential: claim.LeaseCredential, Path: path, Offset: offset, Limit: limit}, &result)
	return result, err
}

func (c *GeneratorWorkspaceClient) WriteFile(ctx context.Context, claim generation.Claim, path, content string) error {
	return c.post(ctx, claim, "generator/files/write", struct {
		generation.LeaseCredential
		Path    string `json:"path"`
		Content string `json:"content"`
	}{LeaseCredential: claim.LeaseCredential, Path: path, Content: content}, nil)
}

func (c *GeneratorWorkspaceClient) Execute(ctx context.Context, claim generation.Claim, command string, consume func(app.ExecuteEvent) error) error {
	if c == nil || c.server == nil {
		return errors.New("generator internal client is not configured")
	}
	err := c.server.PostStream(ctx, generationWorkflowPath(claim.Workflow.ID, "generator/execute"), struct {
		generation.LeaseCredential
		Command string `json:"command"`
	}{LeaseCredential: claim.LeaseCredential, Command: command}, func(raw json.RawMessage) error {
		var event app.ExecuteEvent
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
	return mapGenerationWorkflowError(err)
}

func (c *GeneratorWorkspaceClient) ArchiveWorkspace(ctx context.Context, claim generation.Claim) (app.ArchiveResponse, error) {
	var result app.ArchiveResponse
	err := c.postLong(ctx, claim, "generator/archive", claim.LeaseCredential, &result)
	return result, err
}

func (c *GeneratorWorkspaceClient) post(ctx context.Context, claim generation.Claim, suffix string, body, output any) error {
	if c == nil || c.server == nil || !claim.Valid() {
		return errors.New("generator internal client requires a valid workflow claim")
	}
	return mapGenerationWorkflowError(c.server.Post(ctx, generationWorkflowPath(claim.Workflow.ID, suffix), body, output))
}

func (c *GeneratorWorkspaceClient) postLong(ctx context.Context, claim generation.Claim, suffix string, body, output any) error {
	if c == nil || c.server == nil || !claim.Valid() {
		return errors.New("generator internal client requires a valid workflow claim")
	}
	return mapGenerationWorkflowError(c.server.PostLong(ctx, generationWorkflowPath(claim.Workflow.ID, suffix), body, output))
}
