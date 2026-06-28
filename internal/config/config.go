package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Port         int       `yaml:"port"`
	ProxyPort    int       `yaml:"proxy_port"`
	DataDir      string    `yaml:"data_dir"`
	Kubeconfig   string    `yaml:"kubeconfig"`
	Registry     string    `yaml:"registry"`
	Namespace string `yaml:"acr_namespace"`
	LLM          LLMConfig `yaml:"llm"`
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
		Port:         9090,
		ProxyPort:    3128,
		DataDir:      "/var/lib/breakfix",
		Namespace: "breakfix",
		LLM: LLMConfig{
			BaseURL:    "https://api.deepseek.com/anthropic",
			Model:      "deepseek-v4-pro",
			HaikuModel: "deepseek-v4-flash",
			Effort:     "max",
		},
	}
}

// ImageURL prepends registry to image name if not already a full URL.
func (c Config) ImageURL(image string) string {
	if c.Registry == "" || strings.Contains(image, ".") {
		return image
	}
	return c.Registry + "/" + c.Namespace + "/" + image
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
