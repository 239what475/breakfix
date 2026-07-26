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
	Port                 int         `yaml:"port"`
	ProxyPort            int         `yaml:"proxy_port"`
	HealthPort           int         `yaml:"health_port"`
	DataDir              string      `yaml:"data_dir"`
	DatabaseURL          string      `yaml:"database_url"`
	AgentDatabaseURL     string      `yaml:"agent_database_url"`
	AgentDatabaseRole    string      `yaml:"agent_database_role"`
	Kubeconfig           string      `yaml:"kubeconfig"`
	RegistryAddr         string      `yaml:"registry_addr"`
	RegistryInsecure     bool        `yaml:"registry_insecure"`
	K8sBaseImage         string      `yaml:"k8s_base_image"`
	VClusterBinary       string      `yaml:"vcluster_binary"`
	VClusterChartRepo    string      `yaml:"vcluster_chart_repo"`
	VClusterChartVersion string      `yaml:"vcluster_chart_version"`
	ServerHost           string      `yaml:"server_host"`
	Namespace            string      `yaml:"namespace"`
	CRDNamespace         string      `yaml:"crd_namespace"`
	CooldownMinutes      int         `yaml:"cooldown_minutes"`
	JWTSecret            string      `yaml:"jwt_secret"`
	InternalAPIKey       string      `yaml:"internal_api_key"`
	Agent                AgentConfig `yaml:"agent"`
}

type AgentConfig struct {
	BaseURL        string `yaml:"base_url"`
	APIKeyEnv      string `yaml:"api_key_env"`
	Model          string `yaml:"model"`
	RequestTimeout string `yaml:"request_timeout"`
	ServerURL      string `yaml:"server_url"`
	APIKey         string `yaml:"-"`
}

func defaults() Config {
	return Config{
		Port:              9090,
		ProxyPort:         3128,
		HealthPort:        8081,
		DataDir:           "/var/lib/breakfix",
		DatabaseURL:       "postgres://breakfix:breakfix@postgresql:5432/breakfix?sslmode=disable",
		AgentDatabaseRole: "breakfix_agent",
		RegistryAddr:      "172.18.0.1:5000/break-fix",
		RegistryInsecure:  true,
		K8sBaseImage:      "breakfix-k8s-base:latest",
		VClusterBinary:    "vcluster",
		VClusterChartRepo: "https://charts.loft.sh",
		Namespace:         "breakfix",
		CRDNamespace:      "breakfix-system",
		JWTSecret:         "breakfix-dev-secret-change-in-production",
		InternalAPIKey:    "breakfix-dev-internal-key-change-in-production",
		CooldownMinutes:   5,
		Agent: AgentConfig{
			BaseURL:        "https://api.deepseek.com",
			APIKeyEnv:      "DEEPSEEK_API_KEY",
			Model:          "deepseek-v4-pro",
			RequestTimeout: "2m",
			ServerURL:      "http://breakfix-server",
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
	cfg.Agent.ServerURL = os.ExpandEnv(cfg.Agent.ServerURL)
	if err := applyRuntimeEnvironment(&cfg); err != nil {
		return cfg, err
	}
	cfg.Agent.APIKey = os.Getenv(cfg.Agent.APIKeyEnv)
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
