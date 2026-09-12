// Package mcpconnector contains the local stdio MCP connector. It is an
// authenticated, thin client of the Server Generator HTTP application API.
package mcpconnector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	api "github.com/breakfix/breakfix/internal/transport/httpapi/generated"
)

// Client calls only the public Generator application API. It intentionally
// has no access to Server storage, provider SDKs, or workspace identities.
type Client struct {
	baseURL *url.URL
	token   string
	http    *http.Client
}

type HTTPError struct {
	StatusCode int
	Message    string
}

func (e *HTTPError) Error() string {
	if e == nil {
		return "generator API request failed"
	}
	return fmt.Sprintf("generator API request failed (%d): %s", e.StatusCode, e.Message)
}

func (e *HTTPError) HTTPStatusCode() int {
	if e == nil {
		return 0
	}
	return e.StatusCode
}

func NewClient(serverURL, token string, httpClient *http.Client) (*Client, error) {
	serverURL = strings.TrimSpace(serverURL)
	token = strings.TrimSpace(token)
	if serverURL == "" || token == "" {
		return nil, errors.New("MCP connector requires server URL and user token")
	}
	parsed, err := url.Parse(serverURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("MCP connector server URL must be an absolute HTTP(S) URL without credentials or query")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("MCP connector server URL must use HTTP or HTTPS")
	}
	if parsed.Path == "" || parsed.Path == "/" {
		parsed.Path = "/api"
	} else {
		parsed.Path = strings.TrimRight(parsed.Path, "/")
	}
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	return &Client{baseURL: parsed, token: token, http: httpClient}, nil
}

func (c *Client) SetGenerationPlan(ctx context.Context, request api.GeneratorPlanRequest) (api.GeneratorPlanResponse, error) {
	var response api.GeneratorPlanResponse
	err := c.doJSON(ctx, http.MethodPost, "/generator/plans", nil, request, &response)
	return response, err
}

func (c *Client) ConfirmGeneration(ctx context.Context, request api.GeneratorGenerationConfirmationRequest) (api.GeneratorWorkflow, error) {
	var response api.GeneratorWorkflow
	err := c.doJSON(ctx, http.MethodPost, "/generator/workflows", nil, request, &response)
	return response, err
}

func (c *Client) ListActiveGenerations(ctx context.Context) (api.GeneratorWorkflowList, error) {
	var response api.GeneratorWorkflowList
	err := c.doJSON(ctx, http.MethodGet, "/generator/workflows", nil, nil, &response)
	return response, err
}

func (c *Client) GetGeneration(ctx context.Context, workflowID string) (api.GeneratorGeneration, error) {
	var response api.GeneratorGeneration
	err := c.doJSON(ctx, http.MethodGet, "/generator/workflows/"+url.PathEscape(strings.TrimSpace(workflowID)), nil, nil, &response)
	return response, err
}

func (c *Client) StartWorkspaceTurn(ctx context.Context, workflowID string, request api.GeneratorWorkspaceTurnRequest) (api.GeneratorWorkspaceTurn, error) {
	var response api.GeneratorWorkspaceTurn
	err := c.doJSON(ctx, http.MethodPost, "/generator/workflows/"+url.PathEscape(strings.TrimSpace(workflowID))+"/workspace/turn", nil, request, &response)
	return response, err
}

func (c *Client) EndWorkspaceTurn(ctx context.Context, workflowID string, request api.GeneratorWorkspaceTurnRequest) error {
	return c.doJSON(ctx, http.MethodPost, "/generator/workflows/"+url.PathEscape(strings.TrimSpace(workflowID))+"/workspace/turn/end", nil, request, nil)
}

func (c *Client) ListWorkspaceFiles(ctx context.Context, workflowID, turnID string) (api.GeneratorWorkspaceFileList, error) {
	var response api.GeneratorWorkspaceFileList
	query := url.Values{"turn_id": []string{turnID}}
	err := c.doJSON(ctx, http.MethodGet, "/generator/workflows/"+url.PathEscape(strings.TrimSpace(workflowID))+"/workspace/files", query, nil, &response)
	return response, err
}

func (c *Client) ReadWorkspaceFile(ctx context.Context, workflowID, turnID, filePath string, offset, limit *int) (api.GeneratorWorkspaceFileRead, error) {
	var response api.GeneratorWorkspaceFileRead
	query := url.Values{"turn_id": []string{turnID}, "path": []string{filePath}}
	if offset != nil {
		query.Set("offset", fmt.Sprintf("%d", *offset))
	}
	if limit != nil {
		query.Set("limit", fmt.Sprintf("%d", *limit))
	}
	err := c.doJSON(ctx, http.MethodGet, "/generator/workflows/"+url.PathEscape(strings.TrimSpace(workflowID))+"/workspace/file", query, nil, &response)
	return response, err
}

func (c *Client) WriteWorkspaceFile(ctx context.Context, workflowID string, request api.GeneratorWorkspaceFileWriteRequest) error {
	return c.doJSON(ctx, http.MethodPut, "/generator/workflows/"+url.PathEscape(strings.TrimSpace(workflowID))+"/workspace/files", nil, request, nil)
}

func (c *Client) RunWorkspaceCommand(ctx context.Context, workflowID string, request api.GeneratorWorkspaceCommandRequest) (api.GeneratorWorkspaceCommandResult, error) {
	var response api.GeneratorWorkspaceCommandResult
	err := c.doJSON(ctx, http.MethodPost, "/generator/workflows/"+url.PathEscape(strings.TrimSpace(workflowID))+"/workspace/commands", nil, request, &response)
	return response, err
}

func (c *Client) SubmitCandidate(ctx context.Context, workflowID string, request api.GeneratorCandidateSubmissionRequest) (api.GeneratorGeneration, error) {
	var response api.GeneratorGeneration
	err := c.doJSON(ctx, http.MethodPost, "/generator/workflows/"+url.PathEscape(strings.TrimSpace(workflowID))+"/candidate", nil, request, &response)
	return response, err
}

func (c *Client) ConfirmContent(ctx context.Context, workflowID string, request api.GeneratorContentConfirmationRequest) (api.GeneratorWorkflow, error) {
	var response api.GeneratorWorkflow
	err := c.doJSON(ctx, http.MethodPost, "/generator/workflows/"+url.PathEscape(strings.TrimSpace(workflowID))+"/content/confirm", nil, request, &response)
	return response, err
}

func (c *Client) RequestContentChanges(ctx context.Context, workflowID string, request api.GeneratorContentChangeRequest) (api.GeneratorWorkflow, error) {
	var response api.GeneratorWorkflow
	err := c.doJSON(ctx, http.MethodPost, "/generator/workflows/"+url.PathEscape(strings.TrimSpace(workflowID))+"/content/changes", nil, request, &response)
	return response, err
}

func (c *Client) CancelGeneration(ctx context.Context, workflowID string, request api.GeneratorCancellationRequest) (api.GeneratorWorkflow, error) {
	var response api.GeneratorWorkflow
	err := c.doJSON(ctx, http.MethodPost, "/generator/workflows/"+url.PathEscape(strings.TrimSpace(workflowID))+"/cancel", nil, request, &response)
	return response, err
}

func (c *Client) GetReviewBundle(ctx context.Context, workflowID string, kind api.GetGeneratorReviewBundleParamsKind) (api.GeneratorReviewBundle, error) {
	var response api.GeneratorReviewBundle
	query := url.Values{"kind": []string{string(kind)}}
	err := c.doJSON(ctx, http.MethodGet, "/generator/workflows/"+url.PathEscape(strings.TrimSpace(workflowID))+"/review-bundle", query, nil, &response)
	return response, err
}

func (c *Client) doJSON(ctx context.Context, method, endpoint string, query url.Values, input, output any) error {
	if c == nil || c.baseURL == nil || c.http == nil || strings.TrimSpace(c.token) == "" {
		return errors.New("MCP connector client is not configured")
	}
	requestURL := *c.baseURL
	requestURL.Path = strings.TrimRight(requestURL.Path, "/") + endpoint
	requestURL.RawQuery = query.Encode()
	var body io.Reader
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			return fmt.Errorf("encode generator request: %w", err)
		}
		body = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(ctx, method, requestURL.String(), body)
	if err != nil {
		return fmt.Errorf("create generator request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("call generator API: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		data, _ := io.ReadAll(response.Body)
		var failure api.ErrorResponse
		if json.Unmarshal(data, &failure) != nil || strings.TrimSpace(failure.Error) == "" {
			failure.Error = response.Status
		}
		return &HTTPError{StatusCode: response.StatusCode, Message: failure.Error}
	}
	if output == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.NewDecoder(response.Body).Decode(output); err != nil {
		return fmt.Errorf("decode generator response: %w", err)
	}
	return nil
}
