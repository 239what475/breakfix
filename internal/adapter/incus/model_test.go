package incus

import (
	"errors"
	"testing"
)

func TestConfigRejectsUnsafeConnectionAndMutableFingerprint(t *testing.T) {
	valid := testConfig()
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid config: %v", err)
	}
	for name, mutate := range map[string]func(*Config){
		"http endpoint":         func(c *Config) { c.Endpoint = "http://incus.example:8443" },
		"default project":       func(c *Config) { c.ImageProject = "default" },
		"same projects":         func(c *Config) { c.ImageProject = c.BuildProject },
		"unsupported network":   func(c *Config) { c.NetworkDriver = "ovn" },
		"short fingerprint":     func(c *Config) { c.BaseImageFingerprint = "deadbeef" },
		"missing egress policy": func(c *Config) { c.BlockedEgressCIDRs = nil },
		"invalid network pool":  func(c *Config) { c.NodeNetworkPool = "203.0.113.0/24" },
		"small network subnet":  func(c *Config) { c.NodeNetworkPrefix = 29 },
	} {
		t.Run(name, func(t *testing.T) {
			config := valid
			mutate(&config)
			if err := config.Validate(); !errors.Is(err, ErrInvalid) {
				t.Fatalf("Validate() = %v, want invalid", err)
			}
		})
	}
}

func TestProviderNamesAreOpaqueAndDeterministic(t *testing.T) {
	first, err := NamesForEnvironment("bf", "environment-550e8400-e29b-41d4-a716-446655440000")
	if err != nil {
		t.Fatal(err)
	}
	second, err := NamesForEnvironment("bf", "environment-550e8400-e29b-41d4-a716-446655440000")
	if err != nil {
		t.Fatal(err)
	}
	if first != second || first.Project == "" || first.Project == "environment-550e8400-e29b-41d4-a716-446655440000" {
		t.Fatalf("environment names = %#v, %#v", first, second)
	}
	if len(first.Network) > 15 {
		t.Fatalf("network name %q exceeds Linux interface limit", first.Network)
	}
	node, err := NameForNode("bf", "environment-550e8400-e29b-41d4-a716-446655440000", "proxy")
	if err != nil {
		t.Fatal(err)
	}
	if node == "" || node == "proxy" {
		t.Fatalf("node name = %q", node)
	}
}

func testConfig() Config {
	return Config{
		Endpoint: "https://incus.example:8443",
		TLS: TLSConfig{
			ServerCertificateFile: "/run/secrets/incus/server.crt",
			ClientCertificateFile: "/run/secrets/incus/client.crt",
			ClientKeyFile:         "/run/secrets/incus/client.key",
		},
		StoragePool: "local", NetworkDriver: "bridge", BuildProject: "breakfix-build", ImageProject: "breakfix-images",
		BaseImageAlias: "node-systemd-base-v1", BaseImageFingerprint: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		NamePrefix: "bf", MaxNodesPerEnvironment: 4, NodeCPU: "1", NodeMemory: "512MiB", NodeProcesses: 512, NodeRootDisk: "5GiB",
		NodeNetworkPool: "10.240.0.0/16", NodeNetworkPrefix: 24,
		BlockedEgressCIDRs: []string{"10.0.0.0/8", "169.254.0.0/16"},
	}
}
