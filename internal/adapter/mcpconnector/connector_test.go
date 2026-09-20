package mcpconnector

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/domain/toolresult"
	api "github.com/breakfix/breakfix/internal/transport/httpapi/generated"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestConnectorRegistersGeneratorToolsAndProjectsContentReview(t *testing.T) {
	fake := newConnectorAPI(t)
	projector, err := NewReviewProjector(filepath.Join(t.TempDir(), "reviews"))
	if err != nil {
		t.Fatalf("new review projector: %v", err)
	}
	connector, err := NewConnector(fake, projector, ConnectorConfig{PollInterval: time.Millisecond})
	if err != nil {
		t.Fatalf("new connector: %v", err)
	}
	server, err := connector.NewMCPServer()
	if err != nil {
		t.Fatalf("new MCP server: %v", err)
	}
	session := connectMCP(t, server)

	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("list MCP tools: %v", err)
	}
	got := make([]string, 0, len(tools.Tools))
	for _, tool := range tools.Tools {
		got = append(got, tool.Name)
	}
	for _, required := range []string{
		"set_generation_plan", "confirm_generation", "list_active_generations", "get_generation",
		"list_workspace_files", "read_workspace_file", "write_workspace_file", "run_workspace_command",
		"submit_candidate", "wait_generation", "sync_review", "confirm_content", "request_content_changes", "cancel_generation",
	} {
		if !slices.Contains(got, required) {
			t.Fatalf("registered tools %#v do not contain %q", got, required)
		}
	}

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "get_generation", Arguments: map[string]any{"workflow_id": fake.generation.Workflow.Id},
	})
	if err != nil {
		t.Fatalf("call get_generation: %v", err)
	}
	if result.IsError {
		t.Fatalf("get_generation returned tool error: %#v", result.Content)
	}
	var response generationResult
	decodeMCPResult(t, result, &response)
	if response.Review == nil || response.Review.Kind != "content" || response.Review.ReviewPath == "" {
		t.Fatalf("generation review projection = %#v", response)
	}
	if fake.reviewKind != api.GetGeneratorReviewBundleParamsKindContent {
		t.Fatalf("review bundle kind = %q", fake.reviewKind)
	}
	assertReviewProjection(t, response.Review.ReviewPath, fake.bundle.Manifest, "local-user-token", "sandbox_id", "pvc_name")
	assertNoSensitiveMCPResult(t, result, "local-user-token", "sandbox_id", "pvc_name", "10.0.0.1")
}

func TestConnectorWaitGenerationReturnsCurrentReview(t *testing.T) {
	fake := newConnectorAPI(t)
	projector, err := NewReviewProjector(filepath.Join(t.TempDir(), "reviews"))
	if err != nil {
		t.Fatalf("new review projector: %v", err)
	}
	connector, err := NewConnector(fake, projector, ConnectorConfig{PollInterval: time.Millisecond})
	if err != nil {
		t.Fatalf("new connector: %v", err)
	}
	server, err := connector.NewMCPServer()
	if err != nil {
		t.Fatalf("new MCP server: %v", err)
	}
	session := connectMCP(t, server)

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "wait_generation", Arguments: map[string]any{"workflow_id": fake.generation.Workflow.Id, "timeout_seconds": 1},
	})
	if err != nil {
		t.Fatalf("call wait_generation: %v", err)
	}
	if result.IsError {
		t.Fatalf("wait_generation returned tool error: %#v", result.Content)
	}
	var response generationWaitResult
	decodeMCPResult(t, result, &response)
	if response.Review == nil || response.TimedOut {
		t.Fatalf("wait result = %#v", response)
	}
}

func TestConnectorRedactsRemoteErrorDetails(t *testing.T) {
	fake := newConnectorAPI(t)
	fake.getGenerationErr = &HTTPError{StatusCode: 500, Message: "sandbox_id=sandbox-secret token=local-user-token 10.0.0.1"}
	projector, err := NewReviewProjector(filepath.Join(t.TempDir(), "reviews"))
	if err != nil {
		t.Fatalf("new review projector: %v", err)
	}
	connector, err := NewConnector(fake, projector, ConnectorConfig{})
	if err != nil {
		t.Fatalf("new connector: %v", err)
	}
	server, err := connector.NewMCPServer()
	if err != nil {
		t.Fatalf("new MCP server: %v", err)
	}
	session := connectMCP(t, server)

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "get_generation", Arguments: map[string]any{"workflow_id": fake.generation.Workflow.Id},
	})
	if err != nil {
		t.Fatalf("call get_generation: %v", err)
	}
	if result.IsError {
		t.Fatalf("get_generation returned MCP protocol error: %#v", result)
	}
	var envelope toolresult.Envelope
	decodeMCPEnvelope(t, result, &envelope)
	if envelope.Status != toolresult.Unknown {
		t.Fatalf("error envelope status = %q", envelope.Status)
	}
	assertNoSensitiveMCPResult(t, result, "sandbox-secret", "local-user-token", "10.0.0.1")
	if !strings.Contains(mcpResultText(result), "Breakfix server could not complete") {
		t.Fatalf("tool error = %q", mcpResultText(result))
	}
}

func connectMCP(t *testing.T, server *mcp.Server) *mcp.ClientSession {
	t.Helper()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatalf("connect MCP server: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "breakfix-mcp-test-client", Version: "0.1.0"}, nil)
	session, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatalf("connect MCP client: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func decodeMCPResult(t *testing.T, result *mcp.CallToolResult, target any) {
	t.Helper()
	var envelope toolresult.Envelope
	decodeMCPEnvelope(t, result, &envelope)
	if envelope.Status != toolresult.Succeeded {
		t.Fatalf("MCP result status = %q, error = %q", envelope.Status, envelope.Error)
	}
	if err := json.Unmarshal(envelope.Data, target); err != nil {
		t.Fatalf("decode MCP result data: %q: %v", string(envelope.Data), err)
	}
}

func decodeMCPEnvelope(t *testing.T, result *mcp.CallToolResult, target *toolresult.Envelope) {
	t.Helper()
	if result == nil || len(result.Content) != 1 {
		t.Fatalf("unexpected MCP result %#v", result)
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("MCP result content = %T", result.Content[0])
	}
	if err := json.Unmarshal([]byte(text.Text), target); err != nil {
		t.Fatalf("decode MCP result %q: %v", text.Text, err)
	}
}

func assertNoSensitiveMCPResult(t *testing.T, result *mcp.CallToolResult, forbidden ...string) {
	t.Helper()
	content := mcpResultText(result)
	for _, value := range forbidden {
		if strings.Contains(content, value) {
			t.Fatalf("MCP result leaked %q: %s", value, content)
		}
	}
}

func mcpResultText(result *mcp.CallToolResult) string {
	if result == nil {
		return ""
	}
	var values []string
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			values = append(values, text.Text)
		}
	}
	return strings.Join(values, "\n")
}

type connectorAPI struct {
	generation       api.GeneratorGeneration
	bundle           api.GeneratorReviewBundle
	reviewKind       api.GetGeneratorReviewBundleParamsKind
	getGenerationErr error
}

func newConnectorAPI(t *testing.T) *connectorAPI {
	t.Helper()
	bundle := testReviewBundle(t, "content", "NeedsAuthorReview", map[string]string{
		"overview.md": "# Candidate\n", "judge.md": "# Judge\n",
		"checkpoints/ready.md": "# Ready\n", "candidate/problem.md": "# Problem\n",
	})
	workflow := api.GeneratorWorkflow{Id: "generation-workflow-one", SessionId: "authoring-session-one", PlanRevision: 1, State: "NeedsAuthorReview", StateVersion: 4, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	candidate := &api.AuthoringCandidate{Id: "candidate-revision-one", ArchiveDigest: bundle.Manifest.CandidateArchiveDigest, ContentRevision: "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"}
	return &connectorAPI{generation: api.GeneratorGeneration{Workflow: workflow, Candidate: candidate, Assets: []api.AuthoringAsset{}, Diff: []api.AuthoringFileDiff{}}, bundle: bundle}
}

func (s *connectorAPI) SetGenerationPlan(context.Context, api.GeneratorPlanRequest) (api.GeneratorPlanResponse, error) {
	return api.GeneratorPlanResponse{}, errors.New("unexpected set generation plan")
}

func (s *connectorAPI) ConfirmGeneration(context.Context, api.GeneratorGenerationConfirmationRequest) (api.GeneratorWorkflow, error) {
	return api.GeneratorWorkflow{}, errors.New("unexpected confirm generation")
}

func (s *connectorAPI) ListActiveGenerations(context.Context) (api.GeneratorWorkflowList, error) {
	return api.GeneratorWorkflowList{Workflows: []api.GeneratorWorkflow{s.generation.Workflow}}, nil
}

func (s *connectorAPI) GetGeneration(context.Context, string) (api.GeneratorGeneration, error) {
	if s.getGenerationErr != nil {
		return api.GeneratorGeneration{}, s.getGenerationErr
	}
	return s.generation, nil
}

func (*connectorAPI) StartWorkspaceTurn(context.Context, string, api.GeneratorWorkspaceTurnRequest) (api.GeneratorWorkspaceTurn, error) {
	return api.GeneratorWorkspaceTurn{}, errors.New("unexpected start workspace turn")
}

func (*connectorAPI) EndWorkspaceTurn(context.Context, string, api.GeneratorWorkspaceTurnRequest) error {
	return errors.New("unexpected end workspace turn")
}

func (*connectorAPI) ListWorkspaceFiles(context.Context, string, string) (api.GeneratorWorkspaceFileList, error) {
	return api.GeneratorWorkspaceFileList{}, errors.New("unexpected list workspace files")
}

func (*connectorAPI) ReadWorkspaceFile(context.Context, string, string, string, *int, *int) (api.GeneratorWorkspaceFileRead, error) {
	return api.GeneratorWorkspaceFileRead{}, errors.New("unexpected read workspace file")
}

func (*connectorAPI) WriteWorkspaceFile(context.Context, string, api.GeneratorWorkspaceFileWriteRequest) error {
	return errors.New("unexpected write workspace file")
}

func (*connectorAPI) RunWorkspaceCommand(context.Context, string, api.GeneratorWorkspaceCommandRequest) (api.GeneratorWorkspaceCommandResult, error) {
	return api.GeneratorWorkspaceCommandResult{}, errors.New("unexpected workspace command")
}

func (*connectorAPI) SubmitCandidate(context.Context, string, api.GeneratorCandidateSubmissionRequest) (api.GeneratorGeneration, error) {
	return api.GeneratorGeneration{}, errors.New("unexpected submit candidate")
}

func (*connectorAPI) ConfirmContent(context.Context, string, api.GeneratorContentConfirmationRequest) (api.GeneratorWorkflow, error) {
	return api.GeneratorWorkflow{}, errors.New("unexpected confirm content")
}

func (*connectorAPI) RequestContentChanges(context.Context, string, api.GeneratorContentChangeRequest) (api.GeneratorWorkflow, error) {
	return api.GeneratorWorkflow{}, errors.New("unexpected request content changes")
}

func (*connectorAPI) CancelGeneration(context.Context, string, api.GeneratorCancellationRequest) (api.GeneratorWorkflow, error) {
	return api.GeneratorWorkflow{}, errors.New("unexpected cancel generation")
}

func (s *connectorAPI) GetReviewBundle(_ context.Context, _ string, kind api.GetGeneratorReviewBundleParamsKind) (api.GeneratorReviewBundle, error) {
	s.reviewKind = kind
	return s.bundle, nil
}
