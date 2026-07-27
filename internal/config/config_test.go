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

func TestLoadExpandsRuntimeSecretEnvironment(t *testing.T) {
	t.Setenv("BREAKFIX_TEST_JWT", "jwt-from-environment")
	t.Setenv("BREAKFIX_TEST_INTERNAL", "internal-from-environment")
	t.Setenv("BREAKFIX_TEST_SANDBOX_URL", "http://opensandbox.test.svc.cluster.local")
	t.Setenv("BREAKFIX_TEST_SANDBOX_NAMESPACE", "opensandbox-test")
	path := filepath.Join(t.TempDir(), "breakfix.yaml")
	if err := os.WriteFile(path, []byte("jwt_secret: ${BREAKFIX_TEST_JWT}\ninternal_api_key: ${BREAKFIX_TEST_INTERNAL}\nopensandbox:\n  base_url: ${BREAKFIX_TEST_SANDBOX_URL}\n  namespace: ${BREAKFIX_TEST_SANDBOX_NAMESPACE}\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.JWTSecret != "jwt-from-environment" || cfg.InternalAPIKey != "internal-from-environment" {
		t.Fatalf("runtime secret expansion = jwt %q, internal %q", cfg.JWTSecret, cfg.InternalAPIKey)
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

func TestDefaultVClusterChartVersionIsPinned(t *testing.T) {
	if got := defaults().VClusterChartVersion; got != "0.35.1" {
		t.Fatalf("default vcluster chart version = %q, want 0.35.1", got)
	}
}
