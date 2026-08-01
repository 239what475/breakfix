package incus

import (
	"net/netip"
	"testing"
)

func TestEnvironmentNetworkUsesConfiguredGatewayCIDR(t *testing.T) {
	request := ProvisionNodeEnvironmentRequest{
		EnvironmentUID:        "environment-550e8400-e29b-41d4-a716-446655440000",
		Revision:              "sha256:test",
		NetworkPolicyRevision: "node-network-v1",
		Identity:              NodeEnvironmentIdentity{Network: "bf-n-test", ACL: "bf-a-test"},
	}
	subnet := netip.MustParsePrefix("10.240.45.0/24")
	network := environmentNetwork(request, subnetGatewayPrefix(subnet).String())
	if got, want := network.Config["ipv4.address"], "10.240.45.1/24"; got != want {
		t.Fatalf("network gateway = %q, want %q", got, want)
	}
	client := &Client{config: testConfig()}
	validated, err := client.validateEnvironmentNetworkGateway(network.Config["ipv4.address"])
	if err != nil {
		t.Fatalf("validate network gateway: %v", err)
	}
	if got, want := validated.String(), network.Config["ipv4.address"]; got != want {
		t.Fatalf("validated network gateway = %q, want %q", got, want)
	}
}

func TestNodeNetworkPoolRejectsOverlappingSubnet(t *testing.T) {
	pool := netip.MustParsePrefix("10.240.0.0/16")
	candidate := childSubnet(pool, 24, 45)
	if candidate.String() != "10.240.45.0/24" {
		t.Fatalf("candidate = %s", candidate)
	}
	if !overlapsAnyPrefix(candidate, []netip.Prefix{netip.MustParsePrefix("10.240.45.1/24")}) {
		t.Fatal("candidate did not overlap configured bridge subnet")
	}
	if overlapsAnyPrefix(candidate, []netip.Prefix{netip.MustParsePrefix("10.240.46.1/24")}) {
		t.Fatal("candidate overlapped adjacent bridge subnet")
	}
}
