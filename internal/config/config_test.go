package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestExampleConfigLoads(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate config test source")
	}
	examplePath := filepath.Join(filepath.Dir(source), "..", "..", "config", "breakfix.example.yaml")
	if _, err := Load(examplePath); err != nil {
		t.Fatalf("load example config: %v", err)
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "breakfix.yaml")
	if err := os.WriteFile(path, []byte("port: 9090\nunknown_setting: true\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() succeeded with an unknown field")
	}
	if !strings.Contains(err.Error(), "unknown_setting") {
		t.Fatalf("Load() error = %q, want unknown field name", err)
	}
}

func TestLoadRequiresAnExplicitConfigFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "missing.yaml"))
	if err == nil || !strings.Contains(err.Error(), "read config") {
		t.Fatalf("Load() error = %v, want missing config failure", err)
	}
}

func TestLoadExpandsRuntimeSecretEnvironment(t *testing.T) {
	t.Setenv("BREAKFIX_TEST_JWT", "jwt-from-environment")
	t.Setenv("BREAKFIX_TEST_INTERNAL", "internal-from-environment")
	t.Setenv("BREAKFIX_TEST_VERIFICATION_GRANT", "verification-grant-from-environment")
	t.Setenv("BREAKFIX_TEST_SANDBOX_URL", "http://opensandbox.test.svc.cluster.local")
	t.Setenv("BREAKFIX_TEST_SANDBOX_NAMESPACE", "opensandbox-test")
	path := filepath.Join(t.TempDir(), "breakfix.yaml")
	if err := os.WriteFile(path, []byte("jwt_secret: ${BREAKFIX_TEST_JWT}\ninternal_api_key: ${BREAKFIX_TEST_INTERNAL}\nverification_grant_key: ${BREAKFIX_TEST_VERIFICATION_GRANT}\nopensandbox:\n  base_url: ${BREAKFIX_TEST_SANDBOX_URL}\n  namespace: ${BREAKFIX_TEST_SANDBOX_NAMESPACE}\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.JWTSecret != "jwt-from-environment" || cfg.InternalAPIKey != "internal-from-environment" {
		t.Fatalf("runtime secret expansion = jwt %q, internal %q", cfg.JWTSecret, cfg.InternalAPIKey)
	}
	if cfg.VerificationGrantKey != "verification-grant-from-environment" {
		t.Fatalf("verification grant key = %q", cfg.VerificationGrantKey)
	}
	if cfg.OpenSandbox.BaseURL != "http://opensandbox.test.svc.cluster.local" || cfg.OpenSandbox.Namespace != "opensandbox-test" {
		t.Fatalf("opensandbox runtime expansion = url %q, namespace %q", cfg.OpenSandbox.BaseURL, cfg.OpenSandbox.Namespace)
	}
}

func TestLoadAppliesRuntimeRegistryOverrides(t *testing.T) {
	t.Setenv("BREAKFIX_REGISTRY_ADDR", "registry.internal.example/breakfix")
	t.Setenv("BREAKFIX_REGISTRY_INSECURE", "true")
	t.Setenv("BREAKFIX_REGISTRY_USERNAME", "controller")
	t.Setenv("BREAKFIX_REGISTRY_PASSWORD", "registry-secret")
	path := filepath.Join(t.TempDir(), "breakfix.yaml")
	if err := os.WriteFile(path, []byte("registry_addr: registry.example.invalid/breakfix\nregistry_insecure: false\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RegistryAddr != "registry.internal.example/breakfix" || !cfg.RegistryInsecure || cfg.RegistryUsername != "controller" || cfg.RegistryPassword != "registry-secret" {
		t.Fatalf("runtime registry configuration = %#v", cfg)
	}
}

func TestLoadRejectsPartialRuntimeRegistryCredentials(t *testing.T) {
	t.Setenv("BREAKFIX_REGISTRY_USERNAME", "controller")
	path := filepath.Join(t.TempDir(), "breakfix.yaml")
	if err := os.WriteFile(path, []byte("port: 9090\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "BREAKFIX_REGISTRY_USERNAME") {
		t.Fatalf("Load() error = %v, want registry credential validation", err)
	}
}

func TestLoadRejectsInvalidRuntimeRegistryInsecureOverride(t *testing.T) {
	t.Setenv("BREAKFIX_REGISTRY_INSECURE", "sometimes")
	path := filepath.Join(t.TempDir(), "breakfix.yaml")
	if err := os.WriteFile(path, []byte("port: 9090\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "BREAKFIX_REGISTRY_INSECURE") {
		t.Fatalf("Load() error = %v, want registry override validation", err)
	}
}

func TestValidateServerRejectsMissingUIOrigin(t *testing.T) {
	cfg := validProcessConfig()
	cfg.UIOrigin = ""
	if err := cfg.ValidateServer(); err == nil || !strings.Contains(err.Error(), "ui_origin") {
		t.Fatalf("ValidateServer() error = %v, want ui_origin validation", err)
	}
	cfg.UIOrigin = "https://app.breakfix.example"
	if err := cfg.ValidateServer(); err != nil {
		t.Fatalf("ValidateServer() = %v", err)
	}
}

func TestLoadDoesNotInjectDevelopmentDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "breakfix.yaml")
	if err := os.WriteFile(path, []byte("port: 9090\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.JWTSecret != "" || cfg.RegistryAddr != "" || cfg.Agent.Model != "" {
		t.Fatalf("Load injected development defaults: %#v", cfg)
	}
	if err := cfg.ValidateServer(); err == nil {
		t.Fatal("partial production config passed server validation")
	}
}

func TestProcessValidationRejectsMissingProcessDependencies(t *testing.T) {
	if err := validProcessConfig().ValidateServer(); err != nil {
		t.Fatalf("valid server config: %v", err)
	}
	if err := validProcessConfig().ValidateController(); err != nil {
		t.Fatalf("valid controller config: %v", err)
	}
	if err := validProcessConfig().ValidateAgentWorker(); err != nil {
		t.Fatalf("valid agent worker config: %v", err)
	}

	server := validProcessConfig()
	server.DatabaseURL = ""
	if err := server.ValidateServer(); err == nil {
		t.Fatal("server accepted missing database URL")
	}
	controller := validProcessConfig()
	controller.ServerHost = ""
	if err := controller.ValidateController(); err == nil {
		t.Fatal("controller accepted missing server host")
	}
	worker := validProcessConfig()
	worker.Agent.APIKey = ""
	if err := worker.ValidateAgentWorker(); err == nil {
		t.Fatal("agent worker accepted missing API key")
	}
}

func TestParsedUIOriginRejectsNonOriginValues(t *testing.T) {
	for _, value := range []string{"", "breakfix.example", "ftp://breakfix.example", "https://breakfix.example/path", "https://user@breakfix.example"} {
		if _, err := (Config{UIOrigin: value}).ParsedUIOrigin(); err == nil {
			t.Fatalf("ParsedUIOrigin accepted %q", value)
		}
	}
}

func TestLoadExpandsHomeKubeconfigPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "breakfix.yaml")
	if err := os.WriteFile(path, []byte("kubeconfig: ~/.kube/config\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Kubeconfig != filepath.Join(home, ".kube", "config") {
		t.Fatalf("kubeconfig = %q", cfg.Kubeconfig)
	}
}

func validProcessConfig() Config {
	return Config{
		Port:                 9090,
		HealthPort:           8081,
		DataDir:              "/var/lib/breakfix",
		DatabaseURL:          "postgres://breakfix.example/breakfix",
		AgentDatabaseURL:     "postgres://breakfix.example/breakfix_agent",
		RegistryAddr:         "registry.breakfix.example/breakfix",
		RegistryPullSecret:   "breakfix-registry-pull",
		RegistryWriteSecret:  "breakfix-registry-write",
		RegistryUsername:     "breakfix",
		RegistryPassword:     "password",
		BuilderImage:         "registry.breakfix.example/builder@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		PublisherImage:       "registry.breakfix.example/publisher@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		VerifierImage:        "registry.breakfix.example/verifier@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		VClusterBinary:       "/usr/local/bin/vcluster",
		VClusterChartRepo:    "https://charts.loft.sh",
		VClusterChartVersion: "0.35.1",
		ServerHost:           "breakfix-server",
		UIOrigin:             "https://app.breakfix.example",
		Namespace:            "breakfix",
		CRDNamespace:         "breakfix-system",
		CooldownMinutes:      5,
		JWTSecret:            "jwt",
		InternalAPIKey:       "internal",
		VerificationGrantKey: "verification-grant",
		Agent: AgentConfig{
			BaseURL:        "https://api.example",
			APIKeyEnv:      "BREAKFIX_TEST_AGENT_KEY",
			APIKey:         "agent-key",
			Model:          "test-model",
			RequestTimeout: "1m",
			ServerURL:      "http://breakfix-server:9090",
		},
		OpenSandbox: OpenSandboxConfig{
			BaseURL:          "http://opensandbox.example",
			APIKeyEnv:        "BREAKFIX_TEST_SANDBOX_KEY",
			APIKey:           "sandbox-key",
			Namespace:        "opensandbox",
			WorkspaceImage:   "ubuntu:22.04",
			WorkspaceStorage: "5Gi",
		},
	}
}
