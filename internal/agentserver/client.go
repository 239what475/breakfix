// Package agentserver implements the authenticated Worker-to-Server transport.
// It owns no domain protocol or privilege beyond the configured internal key.
package agentserver

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
	"time"
)

type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

type ResponseError struct {
	StatusCode int
	Message    string
}

func (e *ResponseError) Error() string {
	return fmt.Sprintf("server internal API: %s", e.Message)
}

func IsStatus(err error, status int) bool {
	var responseErr *ResponseError
	return errors.As(err, &responseErr) && responseErr.StatusCode == status
}

func New(serverURL, apiKey string) (*Client, error) {
	serverURL = strings.TrimRight(strings.TrimSpace(serverURL), "/")
	if serverURL == "" {
		return nil, errors.New("agent server_url is required")
	}
	parsed, err := url.Parse(serverURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, errors.New("agent server_url is invalid")
	}
	if strings.TrimSpace(apiKey) == "" {
		return nil, errors.New("internal API key is required")
	}
	return &Client{baseURL: serverURL, apiKey: apiKey, http: &http.Client{Timeout: 30 * time.Second}}, nil
}

func (c *Client) Post(ctx context.Context, path string, body any, output any) error {
	if c == nil || c.http == nil {
		return errors.New("server internal client is not configured")
	}
	return c.postJSON(ctx, path, body, output, c.http)
}

// PostLong uses the request context as the sole deadline. Candidate archives
// and base OCI images can legitimately take longer than the ordinary internal
// API timeout, while the Workflow deadline still bounds the operation.
func (c *Client) PostLong(ctx context.Context, path string, body any, output any) error {
	if c == nil || c.http == nil {
		return errors.New("server internal client is not configured")
	}
	longHTTP := *c.http
	longHTTP.Timeout = 0
	return c.postJSON(ctx, path, body, output, &longHTTP)
}

func (c *Client) postJSON(ctx context.Context, path string, body any, output any, httpClient *http.Client) error {
	if c == nil || c.http == nil {
		return errors.New("server internal client is not configured")
	}
	if !strings.HasPrefix(path, "/") {
		return errors.New("internal API path must be absolute")
	}
	response, err := c.post(ctx, path, body, httpClient)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if output == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.NewDecoder(response.Body).Decode(output); err != nil {
		return fmt.Errorf("decode internal response: %w", err)
	}
	return nil
}

// PostStream reads a Server-produced NDJSON response. It is deliberately
// separate from Post because long-running sandbox commands must not inherit
// the Client's ordinary 30-second request timeout.
func (c *Client) PostStream(ctx context.Context, path string, body any, consume func(json.RawMessage) error) error {
	if c == nil || c.http == nil {
		return errors.New("server internal client is not configured")
	}
	if consume == nil {
		return errors.New("internal stream consumer is required")
	}
	streamHTTP := *c.http
	streamHTTP.Timeout = 0
	response, err := c.post(ctx, path, body, &streamHTTP)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	decoder := json.NewDecoder(response.Body)
	for {
		var event json.RawMessage
		if err := decoder.Decode(&event); errors.Is(err, io.EOF) {
			return nil
		} else if err != nil {
			return fmt.Errorf("decode internal stream: %w", err)
		}
		if err := consume(event); err != nil {
			return err
		}
	}
}

func (c *Client) post(ctx context.Context, path string, body any, httpClient *http.Client) (*http.Response, error) {
	if c == nil || httpClient == nil {
		return nil, errors.New("server internal client is not configured")
	}
	if !strings.HasPrefix(path, "/") {
		return nil, errors.New("internal API path must be absolute")
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode internal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("create internal request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Breakfix-Internal-Key", c.apiKey)
	response, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call server internal API: %w", err)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		data, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		_ = response.Body.Close()
		var failure struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &failure)
		if failure.Error == "" {
			failure.Error = response.Status
		}
		return nil, &ResponseError{StatusCode: response.StatusCode, Message: failure.Error}
	}
	return response, nil
}
