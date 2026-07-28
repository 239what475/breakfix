package config

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Port                 int               `yaml:"port"`
	ProxyPort            int               `yaml:"proxy_port"`
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
	K8sBaseImage         string            `yaml:"k8s_base_image"`
	VClusterBinary       string            `yaml:"vcluster_binary"`
	VClusterChartRepo    string            `yaml:"vcluster_chart_repo"`
	VClusterChartVersion string            `yaml:"vcluster_chart_version"`
	ServerHost           string            `yaml:"server_host"`
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

func defaults() Config {
	return Config{
		Port:                 9090,
		ProxyPort:            3128,
		HealthPort:           8081,
		DataDir:              "/var/lib/breakfix",
		DatabaseURL:          "postgres://breakfix:breakfix@postgresql:5432/breakfix?sslmode=disable",
		AgentDatabaseRole:    "breakfix_agent",
		RegistryAddr:         "172.18.0.1:5000/break-fix",
		RegistryInsecure:     true,
		RegistryPullSecret:   "breakfix-registry-pull",
		RegistryWriteSecret:  "breakfix-registry-write",
		BuilderImage:         "breakfix-builder:latest",
		PublisherImage:       "breakfix-publisher:latest",
		VerifierImage:        "breakfix-verifier:latest",
		K8sBaseImage:         "breakfix-k8s-base:latest",
		VClusterBinary:       "vcluster",
		VClusterChartRepo:    "https://charts.loft.sh",
		VClusterChartVersion: "0.35.1",
		Namespace:            "breakfix",
		CRDNamespace:         "breakfix-system",
		JWTSecret:            "breakfix-dev-secret-change-in-production",
		InternalAPIKey:       "breakfix-dev-internal-key-change-in-production",
		VerificationGrantKey: "breakfix-dev-verification-grant-key-change-in-production",
		CooldownMinutes:      5,
		Agent: AgentConfig{
			BaseURL:        "https://api.deepseek.com",
			APIKeyEnv:      "DEEPSEEK_API_KEY",
			Model:          "deepseek-v4-pro",
			RequestTimeout: "2m",
			ServerURL:      "http://breakfix-server",
		},
		OpenSandbox: OpenSandboxConfig{
			BaseURL:          "http://opensandbox-server.opensandbox.svc.cluster.local",
			APIKeyEnv:        "OPEN_SANDBOX_API_KEY",
			Namespace:        "opensandbox",
			WorkspaceImage:   "ubuntu:22.04",
			WorkspaceStorage: "5Gi",
		},
	}
}

func (c Config) CooldownDuration() string {
	return fmt.Sprintf("%dm", c.CooldownMinutes)
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

// ImageURL prepends registry to image name if not already a full URL.
func (c Config) ImageURL(image string) string {
	if c.RegistryAddr == "" || strings.Contains(image, "/") {
		return image
	}
	return c.RegistryAddr + "/" + image
}

func Load(path string) (Config, error) {
	cfg := defaults()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, fmt.Errorf("read config: %w", err)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("parse config: %w", err)
	}
	if cfg.Agent.APIKeyEnv == "" {
		return cfg, fmt.Errorf("agent api_key_env is required")
	}
	if _, err := cfg.Agent.Timeout(); err != nil {
		return cfg, err
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
	if strings.TrimSpace(cfg.RegistryAddr) != "" && (strings.TrimSpace(cfg.RegistryPullSecret) == "" || strings.TrimSpace(cfg.RegistryWriteSecret) == "") {
		return cfg, fmt.Errorf("registry pull_secret and write_secret are required when registry_addr is set")
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

func (c Config) CertFile() string      { return filepath.Join(c.DataDir, "ca-cert.pem") }
func (c Config) KeyFile() string       { return filepath.Join(c.DataDir, "ca-key.pem") }
func (c Config) ChallengesDir() string { return filepath.Join(c.DataDir, "challenges") }
