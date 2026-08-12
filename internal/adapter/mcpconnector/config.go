package mcpconnector

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the intentionally small local configuration for breakfix-mcp.
// The bearer token itself remains in the process environment, never in this
// file or in any MCP result.
type Config struct {
	ServerURL string `yaml:"server_url"`
	TokenEnv  string `yaml:"token_env"`
}

// DefaultConfigPath returns the per-user configuration location without
// creating it. Callers may always provide -config explicitly.
func DefaultConfigPath() string {
	if root, err := os.UserConfigDir(); err == nil && strings.TrimSpace(root) != "" {
		return filepath.Join(root, "breakfix", "mcp.yaml")
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		return filepath.Join(home, ".breakfix", "mcp.yaml")
	}
	return ".breakfix-mcp.yaml"
}

// LoadConfig reads a strict connector configuration. It intentionally does
// not return the token: callers obtain it only when constructing an HTTP
// client, which keeps accidental config logging harmless.
func LoadConfig(path string) (Config, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return Config{}, errors.New("MCP connector config path is required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read MCP connector config: %w", err)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var config Config
	if err := decoder.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("parse MCP connector config: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return Config{}, errors.New("MCP connector config contains more than one document")
		}
		return Config{}, fmt.Errorf("parse MCP connector config: %w", err)
	}
	config.ServerURL = strings.TrimSpace(os.ExpandEnv(config.ServerURL))
	config.TokenEnv = strings.TrimSpace(config.TokenEnv)
	if config.ServerURL == "" || config.TokenEnv == "" {
		return Config{}, errors.New("MCP connector config requires server_url and token_env")
	}
	return config, nil
}

// NewClient reads the configured user token and constructs the only remote
// capability owned by the local connector.
func (c Config) NewClient(httpClient *http.Client) (*Client, error) {
	token := strings.TrimSpace(os.Getenv(strings.TrimSpace(c.TokenEnv)))
	if token == "" {
		return nil, fmt.Errorf("MCP connector token environment variable %s is empty", strings.TrimSpace(c.TokenEnv))
	}
	return NewClient(c.ServerURL, token, httpClient)
}
