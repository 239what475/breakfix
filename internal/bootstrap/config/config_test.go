package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/breakfix/breakfix/internal/adapter/incus"
)

func TestExampleConfigLoads(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate config test source")
	}
	examplePath := filepath.Join(filepath.Dir(source), "..", "..", "..", "config", "app", "local.example.yaml")
	if _, err := Load(examplePath); err != nil {
		t.Fatalf("load example config: %v", err)
	}
}

func TestLoadExpandsRuntimeConfiguration(t *testing.T) {
	t.Setenv("BREAKFIX_TEST_JWT", "jwt-from-environment")
	t.Setenv("BREAKFIX_TEST_DEBUG_CREDENTIAL", "debug-from-environment")
	t.Setenv("BREAKFIX_TEST_RUNTIME_WORKER", "runtime-from-environment")
	t.Setenv("BREAKFIX_TEST_WORKER_KEY", "worker-from-environment")
	t.Setenv("BREAKFIX_TEST_SANDBOX_URL", "http://opensandbox.test.svc.cluster.local")
	t.Setenv("BREAKFIX_TEST_SANDBOX_NAMESPACE", "opensandbox-test")
	t.Setenv("BREAKFIX_TEST_INCUS_ENDPOINT", "https://incus.test.example:8443")
	t.Setenv("BREAKFIX_TEST_INCUS_FINGERPRINT", strings.Repeat("a", 64))
	t.Setenv("BREAKFIX_TEST_K8S_IMAGE", "registry.test.example/breakfix/k8s-base@sha256:"+strings.Repeat("b", 64))
	t.Setenv("BREAKFIX_TEST_REGISTRY_REPOSITORY", "registry.test.example/breakfix")
	t.Setenv("BREAKFIX_TEST_REGISTRY_PULL_SECRET", "breakfix-registry-pull")
	t.Setenv("BREAKFIX_TEST_REGISTRY_CA", "/run/config/registry-ca.crt")
	path := filepath.Join(t.TempDir(), "breakfix.yaml")
	content := "jwt_secret: ${BREAKFIX_TEST_JWT}\n" +
		"debug:\n  enabled: true\n  credential_env: BREAKFIX_TEST_DEBUG_CREDENTIAL\n" +
		"internal_workers:\n  runtime: ${BREAKFIX_TEST_RUNTIME_WORKER}\n" +
		"worker:\n  api_key_env: BREAKFIX_TEST_WORKER_KEY\n" +
		"registry:\n  repository: ${BREAKFIX_TEST_REGISTRY_REPOSITORY}\n  pull_secret: ${BREAKFIX_TEST_REGISTRY_PULL_SECRET}\n  trust_bundle_file: ${BREAKFIX_TEST_REGISTRY_CA}\n" +
		"opensandbox:\n  base_url: ${BREAKFIX_TEST_SANDBOX_URL}\n  namespace: ${BREAKFIX_TEST_SANDBOX_NAMESPACE}\n" +
		"incus:\n  endpoint: ${BREAKFIX_TEST_INCUS_ENDPOINT}\n  base_image_fingerprint: ${BREAKFIX_TEST_INCUS_FINGERPRINT}\n" +
		"runtime:\n  k8s:\n    base_image_digest: ${BREAKFIX_TEST_K8S_IMAGE}\n    management_terminal_image: ${BREAKFIX_TEST_K8S_IMAGE}\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.JWTSecret != "jwt-from-environment" || cfg.Debug.Credential != "debug-from-environment" || cfg.InternalWorkers.Runtime != "runtime-from-environment" || cfg.Worker.APIKey != "worker-from-environment" {
		t.Fatalf("runtime secret expansion = jwt %q, debug %q, workers %#v, worker key %q", cfg.JWTSecret, cfg.Debug.Credential, cfg.InternalWorkers, cfg.Worker.APIKey)
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
	if cfg.Registry.Repository != "registry.test.example/breakfix" || cfg.Registry.PullSecret != "breakfix-registry-pull" || cfg.Registry.TrustBundleFile != "/run/config/registry-ca.crt" {
		t.Fatalf("registry runtime expansion = %#v", cfg.Registry)
	}
}

func TestProcessConfigurationsValidate(t *testing.T) {
	cfg := validProcessConfig()
	if err := cfg.ValidateServer(); err != nil {
		t.Fatalf("validate server configuration: %v", err)
	}
	if err := cfg.ValidateController(); err != nil {
		t.Fatalf("validate controller configuration: %v", err)
	}
	if err := cfg.ValidateRuntimeWorker(); err != nil {
		t.Fatalf("validate runtime worker configuration: %v", err)
	}
}

//nolint:gosec // Test-only configuration verifies that real credentials cannot be reused.
func TestServerRejectsDebugCredentialReuse(t *testing.T) {
	base := validProcessConfig()
	identities := []struct {
		name  string
		value string
	}{
		{name: "jwt", value: base.JWTSecret},
		{name: "runtime worker", value: base.InternalWorkers.Runtime},
		{name: "worker", value: base.Worker.APIKey},
		{name: "agent", value: base.Agent.APIKey},
		{name: "opensandbox", value: base.OpenSandbox.APIKey},
		{name: "registry username", value: base.Registry.Username},
		{name: "registry", value: base.Registry.Password},
	}
	for _, identity := range identities {
		t.Run(identity.name, func(t *testing.T) {
			cfg := base
			cfg.Debug = DebugConfig{Enabled: true, CredentialEnv: "BREAKFIX_DEBUG_CREDENTIAL", Credential: identity.value}
			if err := cfg.ValidateServer(); err == nil {
				t.Fatalf("reused %s credential was accepted", identity.name)
			}
		})
	}
}

//nolint:gosec // Test-only placeholder verifies the independent debug credential contract.
func TestServerRequiresIndependentDebugCredentialWhenEnabled(t *testing.T) {
	cfg := validProcessConfig()
	cfg.Debug = DebugConfig{Enabled: true, CredentialEnv: "BREAKFIX_DEBUG_CREDENTIAL"}
	if err := cfg.ValidateServer(); err == nil {
		t.Fatal("enabled debug routes accepted without a credential")
	}

	cfg.Debug.Credential = "debug-only-credential"
	if err := cfg.ValidateServer(); err != nil {
		t.Fatalf("valid independent debug credential rejected: %v", err)
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
		Registry:             RegistryConfig{Repository: "registry.breakfix.example/breakfix", PullSecret: "breakfix-registry-pull", Username: "breakfix", Password: "password"},
		VClusterBinary:       "/usr/local/bin/vcluster",
		VClusterChartRepo:    "https://charts.loft.sh",
		VClusterChartVersion: "0.35.1",
		UIOrigin:             "https://app.breakfix.example",
		Namespace:            "breakfix",
		CRDNamespace:         "breakfix-system",
		CooldownMinutes:      5,
		JWTSecret:            "jwt",
		InternalWorkers:      InternalWorkerKeys{Runtime: "runtime-internal"},
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
		Incus: incus.Config{
			Endpoint: "https://incus.example:8443",
			TLS: incus.TLSConfig{
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
				Network: K8sNetworkConfig{
					PublicEgressCIDR: "0.0.0.0/0",
					ProtectedCIDRs:   []string{"10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.168.0.0/16"},
				},
			},
		},
		Catalog: CatalogConfig{
			ReleaseReference: "registry.example.com/breakfix/catalog@sha256:" + strings.Repeat("c", 64),
		},
	}
}
