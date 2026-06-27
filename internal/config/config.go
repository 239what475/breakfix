package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Port       int    `yaml:"port"`
	MTLSPort   int    `yaml:"mtls_port"`
	ProxyPort  int    `yaml:"proxy_port"`
	DataDir    string `yaml:"data_dir"`
	Kubeconfig string `yaml:"kubeconfig"`
	Registry   string `yaml:"registry"`
}

func defaults() Config {
	return Config{
		Port:      9090,
		MTLSPort:  9533,
		ProxyPort: 3128,
		DataDir:   "/var/lib/breakfix",
	}
}

// ImageURL prepends registry to image name if not already a full URL.
func (c Config) ImageURL(image string) string {
	if c.Registry == "" || strings.Contains(image, ".") {
		return image
	}
	return c.Registry + "/" + image
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

func (c Config) CertFile() string        { return filepath.Join(c.DataDir, "ca-cert.pem") }
func (c Config) KeyFile() string         { return filepath.Join(c.DataDir, "ca-key.pem") }
func (c Config) ChallengesDir() string   { return filepath.Join(c.DataDir, "challenges") }
