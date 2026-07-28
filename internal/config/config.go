package config

import (
	"bytes"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Port                 int               `yaml:"port"`
	HealthPort           int               `yaml:"health_port"`
	DataDir              string            `yaml:"data_dir"`
	DatabaseURL          string            `yaml:"database_url"`
	AgentDatabaseURL     string            `yaml:"agent_database_url"`
	AgentDatabaseRole    string            `yaml:"agent_database_role"`
	Kubeconfig           string            `yaml:"kubeconfig"`
	RegistryAddr         string            `yaml:"registry_addr"`
	RegistryInsecure     bool              `yaml:"registry_insecure"`
	RegistryPullSecret   string            `yaml:"registry_pull_secret"`
	RegistryWriteSecret  string            `yaml:"registry_write_secret"`
	BuilderImage         string            `yaml:"builder_image"`
	PublisherImage       string            `yaml:"publisher_image"`
	VerifierImage        string            `yaml:"verifier_image"`
	RegistryUsername     string            `yaml:"-"`
	RegistryPassword     string            `yaml:"-"`
	VClusterBinary       string            `yaml:"vcluster_binary"`
	VClusterChartRepo    string            `yaml:"vcluster_chart_repo"`
	VClusterChartVersion string            `yaml:"vcluster_chart_version"`
	ServerHost           string            `yaml:"server_host"`
	UIOrigin             string            `yaml:"ui_origin"`
	Namespace            string            `yaml:"namespace"`
	CRDNamespace         string            `yaml:"crd_namespace"`
	CooldownMinutes      int               `yaml:"cooldown_minutes"`
	JWTSecret            string            `yaml:"jwt_secret"`
	InternalAPIKey       string            `yaml:"internal_api_key"`
	VerificationGrantKey string            `yaml:"verification_grant_key"`
	Agent                AgentConfig       `yaml:"agent"`
	OpenSandbox          OpenSandboxConfig `yaml:"opensandbox"`
}

type AgentConfig struct {
	BaseURL        string `yaml:"base_url"`
	APIKeyEnv      string `yaml:"api_key_env"`
	Model          string `yaml:"model"`
	RequestTimeout string `yaml:"request_timeout"`
	ServerURL      string `yaml:"server_url"`
	APIKey         string `yaml:"-"`
}

// OpenSandboxConfig describes the Server-owned Generator workspace plane.
// Its lifecycle key intentionally never appears in the Agent Worker config.
type OpenSandboxConfig struct {
	BaseURL          string `yaml:"base_url"`
	APIKeyEnv        string `yaml:"api_key_env"`
	Namespace        string `yaml:"namespace"`
	WorkspaceImage   string `yaml:"workspace_image"`
	WorkspaceStorage string `yaml:"workspace_storage"`
	APIKey           string `yaml:"-"`
}

func (c AgentConfig) Timeout() (time.Duration, error) {
	value, err := time.ParseDuration(c.RequestTimeout)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("agent request_timeout must be a positive duration")
	}
	return value, nil
}

func (c OpenSandboxConfig) Validate() error {
	if strings.TrimSpace(c.BaseURL) == "" || strings.TrimSpace(c.APIKeyEnv) == "" || strings.TrimSpace(c.Namespace) == "" {
		return fmt.Errorf("opensandbox base_url, api_key_env, and namespace are required")
	}
	if strings.TrimSpace(c.WorkspaceImage) == "" || strings.TrimSpace(c.WorkspaceStorage) == "" {
		return fmt.Errorf("opensandbox workspace_image and workspace_storage are required")
	}
	return nil
}

func Load(path string) (Config, error) {
	var cfg Config
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("read config: %w", err)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("parse config: %w", err)
	}
	cfg.DatabaseURL = os.ExpandEnv(cfg.DatabaseURL)
	cfg.AgentDatabaseURL = os.ExpandEnv(cfg.AgentDatabaseURL)
	cfg.JWTSecret = os.ExpandEnv(cfg.JWTSecret)
	cfg.InternalAPIKey = os.ExpandEnv(cfg.InternalAPIKey)
	cfg.VerificationGrantKey = os.ExpandEnv(cfg.VerificationGrantKey)
	cfg.Agent.ServerURL = os.ExpandEnv(cfg.Agent.ServerURL)
	cfg.RegistryPullSecret = os.ExpandEnv(cfg.RegistryPullSecret)
	cfg.RegistryWriteSecret = os.ExpandEnv(cfg.RegistryWriteSecret)
	cfg.OpenSandbox.BaseURL = os.ExpandEnv(cfg.OpenSandbox.BaseURL)
	cfg.OpenSandbox.Namespace = os.ExpandEnv(cfg.OpenSandbox.Namespace)
	cfg.UIOrigin = os.ExpandEnv(cfg.UIOrigin)
	cfg.Kubeconfig = expandKubeconfigPath(os.ExpandEnv(cfg.Kubeconfig))
	if err := applyRuntimeEnvironment(&cfg); err != nil {
		return cfg, err
	}
	cfg.Agent.APIKey = os.Getenv(cfg.Agent.APIKeyEnv)
	cfg.OpenSandbox.APIKey = os.Getenv(cfg.OpenSandbox.APIKeyEnv)
	cfg.RegistryUsername = os.Getenv("BREAKFIX_REGISTRY_USERNAME")
	cfg.RegistryPassword = os.Getenv("BREAKFIX_REGISTRY_PASSWORD")
	if (strings.TrimSpace(cfg.RegistryUsername) == "") != (strings.TrimSpace(cfg.RegistryPassword) == "") {
		return cfg, fmt.Errorf("BREAKFIX_REGISTRY_USERNAME and BREAKFIX_REGISTRY_PASSWORD must be set together")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return cfg, fmt.Errorf("parse config: multiple YAML documents are not supported")
		}
		return cfg, fmt.Errorf("parse config: %w", err)
	}
	return cfg, nil
}

func expandKubeconfigPath(value string) string {
	value = strings.TrimSpace(value)
	if value != "~" && !strings.HasPrefix(value, "~/") {
		return value
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return value
	}
	if value == "~" {
		return home
	}
	return filepath.Join(home, strings.TrimPrefix(value, "~/"))
}

// ValidateServer checks the complete dependency contract of the Server
// process. Other processes intentionally validate only the configuration they
// consume, so an Agent Worker never needs a Kubernetes or OpenSandbox secret.
func (c Config) ValidateServer() error {
	if c.Port <= 0 || strings.TrimSpace(c.DataDir) == "" || strings.TrimSpace(c.DatabaseURL) == "" {
		return fmt.Errorf("server port, data_dir, and database_url are required")
	}
	if strings.TrimSpace(c.JWTSecret) == "" || strings.TrimSpace(c.InternalAPIKey) == "" || strings.TrimSpace(c.VerificationGrantKey) == "" {
		return fmt.Errorf("server jwt_secret, internal_api_key, and verification_grant_key are required")
	}
	if strings.TrimSpace(c.RegistryAddr) == "" || strings.TrimSpace(c.RegistryPullSecret) == "" || strings.TrimSpace(c.RegistryWriteSecret) == "" {
		return fmt.Errorf("server registry_addr, registry_pull_secret, and registry_write_secret are required")
	}
	if strings.TrimSpace(c.RegistryUsername) == "" || strings.TrimSpace(c.RegistryPassword) == "" {
		return fmt.Errorf("server registry credentials are required")
	}
	if strings.TrimSpace(c.Namespace) == "" || strings.TrimSpace(c.CRDNamespace) == "" || c.CooldownMinutes <= 0 {
		return fmt.Errorf("server namespace, crd_namespace, and positive cooldown_minutes are required")
	}
	if strings.TrimSpace(c.Agent.Model) == "" {
		return fmt.Errorf("server agent model is required")
	}
	if err := c.OpenSandbox.Validate(); err != nil {
		return fmt.Errorf("server opensandbox: %w", err)
	}
	if strings.TrimSpace(c.OpenSandbox.APIKey) == "" {
		return fmt.Errorf("server opensandbox lifecycle API key is required")
	}
	if _, err := c.ParsedUIOrigin(); err != nil {
		return fmt.Errorf("server ui_origin: %w", err)
	}
	return nil
}

func (c Config) ValidateController() error {
	if c.HealthPort <= 0 || c.Port <= 0 || strings.TrimSpace(c.ServerHost) == "" {
		return fmt.Errorf("controller health_port, server port, and server_host are required")
	}
	if strings.TrimSpace(c.Namespace) == "" || strings.TrimSpace(c.CRDNamespace) == "" || c.CooldownMinutes <= 0 {
		return fmt.Errorf("controller namespace, crd_namespace, and positive cooldown_minutes are required")
	}
	if strings.TrimSpace(c.RegistryAddr) == "" || strings.TrimSpace(c.RegistryPullSecret) == "" || strings.TrimSpace(c.RegistryWriteSecret) == "" {
		return fmt.Errorf("controller registry_addr, registry_pull_secret, and registry_write_secret are required")
	}
	if strings.TrimSpace(c.RegistryUsername) == "" || strings.TrimSpace(c.RegistryPassword) == "" {
		return fmt.Errorf("controller registry credentials are required")
	}
	if strings.TrimSpace(c.BuilderImage) == "" || strings.TrimSpace(c.PublisherImage) == "" || strings.TrimSpace(c.VerifierImage) == "" {
		return fmt.Errorf("controller builder_image, publisher_image, and verifier_image are required")
	}
	if strings.TrimSpace(c.VerificationGrantKey) == "" || strings.TrimSpace(c.VClusterBinary) == "" || strings.TrimSpace(c.VClusterChartRepo) == "" || strings.TrimSpace(c.VClusterChartVersion) == "" {
		return fmt.Errorf("controller verification grant and vcluster configuration are required")
	}
	return nil
}

func (c Config) ValidateAgentWorker() error {
	if strings.TrimSpace(c.AgentDatabaseURL) == "" || strings.TrimSpace(c.InternalAPIKey) == "" {
		return fmt.Errorf("agent worker agent_database_url and internal_api_key are required")
	}
	if strings.TrimSpace(c.Agent.BaseURL) == "" || strings.TrimSpace(c.Agent.APIKeyEnv) == "" || strings.TrimSpace(c.Agent.APIKey) == "" || strings.TrimSpace(c.Agent.Model) == "" || strings.TrimSpace(c.Agent.ServerURL) == "" {
		return fmt.Errorf("agent worker base_url, api_key_env, API key, model, and server_url are required")
	}
	if _, err := c.Agent.Timeout(); err != nil {
		return err
	}
	return nil
}

// ParsedUIOrigin accepts exactly one browser origin. Terminal WebSockets use
// it to reject every other Origin before the ticket is consumed.
func (c Config) ParsedUIOrigin() (*url.URL, error) {
	value := strings.TrimSpace(c.UIOrigin)
	if value == "" {
		return nil, fmt.Errorf("ui_origin is required")
	}
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("must be an absolute http or https origin")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("must use http or https")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return nil, fmt.Errorf("must not include credentials, path, query, or fragment")
	}
	parsed.Path = ""
	return parsed, nil
}

// applyRuntimeEnvironment contains deployment-time values that cannot be
// safely committed into the shared in-cluster configuration. Empty variables
// deliberately leave the YAML value intact so local configuration stays
// self-contained.
func applyRuntimeEnvironment(cfg *Config) error {
	if value := strings.TrimSpace(os.Getenv("BREAKFIX_REGISTRY_ADDR")); value != "" {
		cfg.RegistryAddr = value
	}
	if value, exists := os.LookupEnv("BREAKFIX_REGISTRY_INSECURE"); exists && strings.TrimSpace(value) != "" {
		parsed, err := strconv.ParseBool(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("parse BREAKFIX_REGISTRY_INSECURE: %w", err)
		}
		cfg.RegistryInsecure = parsed
	}
	return nil
}

func (c Config) ChallengesDir() string { return filepath.Join(c.DataDir, "challenges") }
