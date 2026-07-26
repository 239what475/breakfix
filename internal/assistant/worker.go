package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/breakfix/breakfix/internal/agentruntime"
	"github.com/breakfix/breakfix/internal/agentworker"
	"github.com/breakfix/breakfix/internal/config"
)

// WorkerExecutor executes the read-only Assistant purpose. Its only direct
// database access is durable Runtime state; all environment reads go through
// RuntimeClient to the Server.
type WorkerExecutor struct {
	repo   agentruntime.Repository
	config config.AgentConfig
	client RuntimeClient
}

func NewWorkerExecutor(repo agentruntime.Repository, cfg config.AgentConfig, client RuntimeClient) (*WorkerExecutor, error) {
	if repo == nil || client == nil {
		return nil, errors.New("assistant worker executor requires runtime repository and server client")
	}
	return &WorkerExecutor{repo: repo, config: cfg, client: client}, nil
}

func (e *WorkerExecutor) Execute(ctx context.Context, claim agentruntime.Claim, emit agentworker.Emitter) (agentworker.ExecutionResult, error) {
	if claim.Run.Purpose != "assistant" || !claim.Valid() {
		return agentworker.ExecutionResult{}, errors.New("invalid assistant agent run claim")
	}
	contextSnapshot, err := e.client.LoadContext(ctx, claim)
	if err != nil {
		return agentworker.ExecutionResult{}, fmt.Errorf("load assistant context: %w", err)
	}
	history, err := e.repo.ListMessages(ctx, claim.Run.SessionID)
	if err != nil {
		return agentworker.ExecutionResult{}, fmt.Errorf("load assistant history: %w", err)
	}
	reader := remoteReader{client: e.client, claim: claim}
	result, err := RunWithEino(ctx, e.config, contextSnapshot.Request(reader), history, func(event Event) {
		switch event.Type {
		case "delta":
			emit.EmitDelta(ctx, event.Content)
		case "tool":
			emit.EmitTool(ctx, event.Tool)
		case "reset":
			emit.EmitReset(ctx)
		}
	})
	if err != nil {
		return agentworker.ExecutionResult{}, err
	}
	metadata, err := json.Marshal(struct {
		Evidence []Evidence `json:"evidence"`
	}{Evidence: result.Evidence})
	if err != nil {
		return agentworker.ExecutionResult{}, fmt.Errorf("encode assistant evidence: %w", err)
	}
	return agentworker.ExecutionResult{Message: &agentruntime.Message{
		ID:        agentruntime.NewID("assistant-message"),
		SessionID: claim.Run.SessionID,
		Role:      "assistant",
		Content:   result.Content,
		Metadata:  metadata,
	}}, nil
}

type remoteReader struct {
	client RuntimeClient
	claim  agentruntime.Claim
}

func (r remoteReader) TerminalScrollback(ctx context.Context, window string, offset, lines int) (Scrollback, error) {
	var result Scrollback
	err := r.client.InvokeTool(ctx, r.claim, "get_terminal_scrollback", struct {
		Window string `json:"window"`
		Offset int    `json:"offset"`
		Lines  int    `json:"lines"`
	}{window, offset, lines}, &result)
	return result, err
}

func (r remoteReader) CheckpointStatus(ctx context.Context) (CheckpointSnapshot, error) {
	var result CheckpointSnapshot
	err := r.client.InvokeTool(ctx, r.claim, "get_checkpoint_status", struct{}{}, &result)
	return result, err
}

func (r remoteReader) ListEnvironmentFiles(ctx context.Context, path string, offset, limit int) (EnvironmentFiles, error) {
	var result EnvironmentFiles
	err := r.client.InvokeTool(ctx, r.claim, "list_environment_files", struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}{path, offset, limit}, &result)
	return result, err
}

func (r remoteReader) ReadEnvironmentFile(ctx context.Context, path string, offset int64, maxBytes int) (EnvironmentFile, error) {
	var result EnvironmentFile
	err := r.client.InvokeTool(ctx, r.claim, "read_environment_file", struct {
		Path     string `json:"path"`
		Offset   int64  `json:"offset"`
		MaxBytes int    `json:"max_bytes"`
	}{path, offset, maxBytes}, &result)
	return result, err
}

func (r remoteReader) Solution(ctx context.Context) (string, error) {
	var result struct {
		Content string `json:"content"`
	}
	err := r.client.InvokeTool(ctx, r.claim, "get_solution", struct{}{}, &result)
	return result.Content, err
}

// DeltaSink keeps partial model output transient by sending it only to the
// Server event hub. A transport failure is deliberately ignored by Worker.
type DeltaSink struct{ Client RuntimeClient }

func (s DeltaSink) EmitDelta(ctx context.Context, claim agentruntime.Claim, content string) error {
	if s.Client == nil {
		return errors.New("assistant delta sink has no server client")
	}
	return s.Client.PublishEvent(ctx, claim, Event{Type: "delta", Content: content})
}

func (s DeltaSink) EmitTool(ctx context.Context, claim agentruntime.Claim, name string) error {
	if s.Client == nil {
		return errors.New("assistant delta sink has no server client")
	}
	return s.Client.PublishEvent(ctx, claim, Event{Type: "tool", Tool: name})
}

func (s DeltaSink) EmitReset(ctx context.Context, claim agentruntime.Claim) error {
	if s.Client == nil {
		return errors.New("assistant delta sink has no server client")
	}
	return s.Client.PublishEvent(ctx, claim, Event{Type: "reset"})
}
