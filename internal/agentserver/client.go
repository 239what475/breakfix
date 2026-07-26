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
	if !strings.HasPrefix(path, "/") {
		return errors.New("internal API path must be absolute")
	}
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode internal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("create internal request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Breakfix-Internal-Key", c.apiKey)
	response, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("call server internal API: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		data, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		var failure struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &failure)
		if failure.Error == "" {
			failure.Error = response.Status
		}
		return fmt.Errorf("server internal API: %s", failure.Error)
	}
	if output == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.NewDecoder(response.Body).Decode(output); err != nil {
		return fmt.Errorf("decode internal response: %w", err)
	}
	return nil
}
