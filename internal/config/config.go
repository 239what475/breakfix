package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Port             int       `yaml:"port"`
	ProxyPort        int       `yaml:"proxy_port"`
	HealthPort       int       `yaml:"health_port"`
	DataDir          string    `yaml:"data_dir"`
	Kubeconfig       string    `yaml:"kubeconfig"`
	RegistryAddr     string    `yaml:"registry_addr"`
	RegistryInsecure bool      `yaml:"registry_insecure"`
	ServerHost       string    `yaml:"server_host"`
	Namespace        string    `yaml:"namespace"`
	CRDNamespace     string    `yaml:"crd_namespace"`
	CooldownMinutes  int       `yaml:"cooldown_minutes"`
	JWTSecret        string    `yaml:"jwt_secret"`
	InternalAPIKey   string    `yaml:"internal_api_key"`
	LLM              LLMConfig `yaml:"llm"`
}

type LLMConfig struct {
	BaseURL    string `yaml:"base_url"`
	Model      string `yaml:"model"`
	HaikuModel string `yaml:"haiku_model"`
	Effort     string `yaml:"effort"`
	APIKey     string `yaml:"api_key"`
}

func defaults() Config {
	return Config{
		Port:            9090,
		ProxyPort:       3128,
		HealthPort:      8081,
		DataDir:          "/var/lib/breakfix",
		RegistryAddr:     "172.18.0.1:5000/break-fix",
		RegistryInsecure: true,
		Namespace:        "breakfix",
		CRDNamespace:    "breakfix-system",
		JWTSecret:       "breakfix-dev-secret-change-in-production",
		InternalAPIKey:  "breakfix-dev-internal-key-change-in-production",
		CooldownMinutes: 5,
		LLM: LLMConfig{
			BaseURL:    "https://api.deepseek.com/anthropic",
			Model:      "deepseek-v4-pro",
			HaikuModel: "deepseek-v4-flash",
			Effort:     "max",
		},
	}
}

func (c Config) CooldownDuration() string {
	return fmt.Sprintf("%dm", c.CooldownMinutes)
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
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse config: %w", err)
	}
	return cfg, nil
}

func (c Config) CertFile() string      { return filepath.Join(c.DataDir, "ca-cert.pem") }
func (c Config) KeyFile() string       { return filepath.Join(c.DataDir, "ca-key.pem") }
func (c Config) ChallengesDir() string { return filepath.Join(c.DataDir, "challenges") }
