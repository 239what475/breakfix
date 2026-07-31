package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/breakfix/breakfix/internal/incusprovider"
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
	t.Setenv("BREAKFIX_TEST_AGENT_WORKER", "agent-from-environment")
	t.Setenv("BREAKFIX_TEST_BUILDER_WORKER", "builder-from-environment")
	t.Setenv("BREAKFIX_TEST_PUBLISHER_WORKER", "publisher-from-environment")
	t.Setenv("BREAKFIX_TEST_VERIFIER_WORKER", "verifier-from-environment")
	t.Setenv("BREAKFIX_TEST_WORKER_KEY", "worker-from-environment")
	t.Setenv("BREAKFIX_TEST_SANDBOX_URL", "http://opensandbox.test.svc.cluster.local")
	t.Setenv("BREAKFIX_TEST_SANDBOX_NAMESPACE", "opensandbox-test")
	t.Setenv("BREAKFIX_TEST_INCUS_ENDPOINT", "https://incus.test.example:8443")
	t.Setenv("BREAKFIX_TEST_INCUS_FINGERPRINT", strings.Repeat("a", 64))
	t.Setenv("BREAKFIX_TEST_K8S_IMAGE", "registry.test.example/breakfix/k8s-base@sha256:"+strings.Repeat("b", 64))
	t.Setenv("BREAKFIX_TEST_REGISTRY_ADDR", "registry.test.example/breakfix")
	t.Setenv("BREAKFIX_TEST_REGISTRY_PULL_SECRET", "breakfix-registry-pull")
	t.Setenv("BREAKFIX_TEST_REGISTRY_CA", "/run/config/registry-ca.crt")
	path := filepath.Join(t.TempDir(), "breakfix.yaml")
	if err := os.WriteFile(path, []byte("jwt_secret: ${BREAKFIX_TEST_JWT}\ninternal_workers:\n  agent: ${BREAKFIX_TEST_AGENT_WORKER}\n  builder: ${BREAKFIX_TEST_BUILDER_WORKER}\n  publisher: ${BREAKFIX_TEST_PUBLISHER_WORKER}\n  verifier: ${BREAKFIX_TEST_VERIFIER_WORKER}\nworker:\n  api_key_env: BREAKFIX_TEST_WORKER_KEY\nregistry:\n  address: ${BREAKFIX_TEST_REGISTRY_ADDR}\n  pull_secret: ${BREAKFIX_TEST_REGISTRY_PULL_SECRET}\n  trust_bundle_file: ${BREAKFIX_TEST_REGISTRY_CA}\nopensandbox:\n  base_url: ${BREAKFIX_TEST_SANDBOX_URL}\n  namespace: ${BREAKFIX_TEST_SANDBOX_NAMESPACE}\nincus:\n  endpoint: ${BREAKFIX_TEST_INCUS_ENDPOINT}\n  base_image_fingerprint: ${BREAKFIX_TEST_INCUS_FINGERPRINT}\nruntime:\n  k8s:\n    base_image_digest: ${BREAKFIX_TEST_K8S_IMAGE}\n    management_terminal_image: ${BREAKFIX_TEST_K8S_IMAGE}\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.JWTSecret != "jwt-from-environment" || cfg.InternalWorkers.Agent != "agent-from-environment" ||
		cfg.InternalWorkers.Builder != "builder-from-environment" || cfg.InternalWorkers.Publisher != "publisher-from-environment" ||
		cfg.InternalWorkers.Verifier != "verifier-from-environment" || cfg.Worker.APIKey != "worker-from-environment" {
		t.Fatalf("runtime secret expansion = jwt %q, workers %#v, worker key %q", cfg.JWTSecret, cfg.InternalWorkers, cfg.Worker.APIKey)
	}
	if cfg.OpenSandbox.BaseURL != "http://opensandbox.test.svc.cluster.local" || cfg.OpenSandbox.Namespace != "opensandbox-test" {
		t.Fatalf("opensandbox runtime expansion = url %q, namespace %q", cfg.OpenSandbox.BaseURL, cfg.OpenSandbox.Namespace)
	}
	if cfg.Incus.Endpoint != "https://incus.test.example:8443" || cfg.Incus.BaseImageFingerprint != strings.Repeat("a", 64) {
		t.Fatalf("incus runtime expansion = endpoint %q, fingerprint %q", cfg.Incus.Endpoint, cfg.Incus.BaseImageFingerprint)
	}
	if cfg.Runtime.K8s.BaseImageDigest != "registry.test.example/breakfix/k8s-base@sha256:"+strings.Repeat("b", 64) ||
		cfg.Runtime.K8s.ManagementTerminalImage != cfg.Runtime.K8s.BaseImageDigest {
		t.Fatalf("k8s runtime image expansion = base %q, terminal %q", cfg.Runtime.K8s.BaseImageDigest, cfg.Runtime.K8s.ManagementTerminalImage)
	}
	if cfg.Registry.Address != "registry.test.example/breakfix" || cfg.Registry.PullSecret != "breakfix-registry-pull" || cfg.Registry.TrustBundleFile != "/run/config/registry-ca.crt" {
		t.Fatalf("registry runtime expansion = %#v", cfg.Registry)
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

func TestRegistryConfigRequiresRepositoryRootRatherThanImageReference(t *testing.T) {
	cfg := validProcessConfig()
	cfg.Registry.Address = "https://registry.breakfix.internal/breakfix"
	if err := cfg.ValidateServer(); err == nil || !strings.Contains(err.Error(), "registry") {
		t.Fatalf("ValidateServer() error = %v, want Registry address validation", err)
	}
	cfg.Registry.Address = "registry.breakfix.internal/breakfix:latest"
	if err := cfg.ValidatePublisherWorker(); err == nil || !strings.Contains(err.Error(), "registry") {
		t.Fatalf("ValidatePublisherWorker() error = %v, want Registry image-tag validation", err)
	}
	cfg.Registry.Address = "registry.breakfix.internal/breakfix"
	cfg.Registry.Username = ""
	cfg.Registry.Password = ""
	if err := cfg.ValidateServer(); err != nil {
		t.Fatalf("ValidateServer() rejected anonymous Registry: %v", err)
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
	if cfg.JWTSecret != "" || cfg.Registry.Address != "" || cfg.Agent.Model != "" {
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
	controller.Registry.PullSecret = ""
	if err := controller.ValidateController(); err != nil {
		t.Fatalf("controller unexpectedly requires a pull secret for an anonymous Registry: %v", err)
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

//nolint:gosec // Test-only placeholder values exercise process configuration validation.
func validProcessConfig() Config {
	return Config{
		Port:                 9090,
		HealthPort:           8081,
		DataDir:              "/var/lib/breakfix",
		DatabaseURL:          "postgres://breakfix.example/breakfix",
		Registry:             RegistryConfig{Address: "registry.breakfix.example/breakfix", PullSecret: "breakfix-registry-pull", Username: "breakfix", Password: "password"},
		VClusterBinary:       "/usr/local/bin/vcluster",
		VClusterChartRepo:    "https://charts.loft.sh",
		VClusterChartVersion: "0.35.1",
		UIOrigin:             "https://app.breakfix.example",
		Namespace:            "breakfix",
		CRDNamespace:         "breakfix-system",
		CooldownMinutes:      5,
		JWTSecret:            "jwt",
		InternalWorkers:      InternalWorkerKeys{Agent: "agent-internal", Builder: "builder-internal", Publisher: "publisher-internal", Verifier: "verifier-internal"},
		Worker:               WorkerConfig{ServerURL: "http://breakfix-server:9090", APIKeyEnv: "BREAKFIX_TEST_WORKER_KEY", APIKey: "worker-internal"},
		Agent: AgentConfig{
			BaseURL:        "https://api.example",
			APIKeyEnv:      "BREAKFIX_TEST_AGENT_KEY",
			APIKey:         "agent-key",
			Model:          "test-model",
			RequestTimeout: "1m",
		},
		OpenSandbox: OpenSandboxConfig{
			BaseURL:                   "http://opensandbox.example",
			APIKeyEnv:                 "BREAKFIX_TEST_SANDBOX_KEY",
			APIKey:                    "sandbox-key",
			Namespace:                 "opensandbox",
			WorkspaceImage:            "ubuntu:22.04",
			WorkspaceStorage:          "5Gi",
			WorkspaceCPU:              "500m",
			WorkspaceMemory:           "512Mi",
			WorkspaceProvisionTimeout: "5m",
		},
		Incus: incusprovider.Config{
			Endpoint: "https://incus.example:8443",
			TLS: incusprovider.TLSConfig{
				ServerCertificateFile: "/var/run/secrets/breakfix-incus/server.crt",
				ClientCertificateFile: "/var/run/secrets/breakfix-incus/client.crt",
				ClientKeyFile:         "/var/run/secrets/breakfix-incus/client.key",
			},
			StoragePool: "local", NetworkDriver: "bridge", BuildProject: "breakfix-build", ImageProject: "breakfix-images",
			BaseImageAlias: "node-systemd-base-v1", BaseImageFingerprint: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			NamePrefix: "bf", MaxNodesPerEnvironment: 4, NodeCPU: "1", NodeMemory: "512MiB", NodeProcesses: 512, NodeRootDisk: "5GiB",
			NodeNetworkPool: "10.240.0.0/16", NodeNetworkPrefix: 24,
			BlockedEgressCIDRs: []string{"10.0.0.0/8", "100.64.0.0/10", "169.254.0.0/16", "172.16.0.0/12", "192.168.0.0/16"},
		},
		Runtime: RuntimeConfig{
			Node: NodeRuntimeConfig{ProfileRevision: "node-profile-v1", NetworkPolicyRevision: "node-network-v1"},
			K8s: K8sRuntimeConfig{
				BaseImageDigest: "registry.example/k8s-base@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				ProfileRevision: "vk8s-profile-v1", Version: "v1.36.2",
				ManagementTerminalImage: "registry.example/k8s-base@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				Resources: K8sResourceConfig{
					ControlPlaneCPU: "500m", ControlPlaneMemory: "512Mi", ControlPlaneEphemeralStorage: "10Gi",
					WorkloadCPU: "500m", WorkloadMemory: "512Mi", WorkloadEphemeralStorage: "3Gi",
					QuotaCPU: "3", QuotaMemory: "3Gi", QuotaEphemeralStorage: "30Gi",
				},
			},
		},
	}
}
