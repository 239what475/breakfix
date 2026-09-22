package incus

import (
	"net/netip"
	"strconv"
	"testing"

	"github.com/lxc/incus/v7/shared/api"
)

// The reset generation fence decides whether a retried reset wipes or adopts:
// a project stamped with the request's own generation is the rebuild that
// reset created, and the stamp never leaks into project policy validation.
func TestProjectResetGenerationFenceAdoptsOnlyTheCurrentRebuild(t *testing.T) {
	reset := ProvisionNodeEnvironmentRequest{EnvironmentUID: "environment-uid", Revision: "revision", ResetNonce: 3, Identity: NodeEnvironmentIdentity{Project: "bf-p-test"}}
	if !projectCarriesResetGeneration(api.ConfigMap{resetGenerationKey: "3"}, reset) {
		t.Fatal("the rebuild of this reset generation was not adopted")
	}
	if projectCarriesResetGeneration(api.ConfigMap{resetGenerationKey: "2"}, reset) {
		t.Fatal("an older generation's project was adopted as the rebuild")
	}
	if projectCarriesResetGeneration(api.ConfigMap{}, reset) {
		t.Fatal("an unstamped project was adopted as the rebuild")
	}
	plain := reset
	plain.ResetNonce = 0
	if projectCarriesResetGeneration(api.ConfigMap{resetGenerationKey: "3"}, plain) {
		t.Fatal("a provision or release request adopted the reset rebuild")
	}

	// A plain provision expects the project policy without the stamp, and a
	// stamped project from an earlier reset must still validate against it.
	stamped, err := (&Client{config: testConfig()}).environmentProject(ProvisionNodeEnvironmentRequest{
		EnvironmentUID: "environment-uid", Revision: "revision", ResetNonce: 3,
		Identity:  NodeEnvironmentIdentity{Project: "bf-p-test", Nodes: []NodeIdentity{{LogicalName: "host"}}},
		Resources: NodeEnvironmentResources{CPU: "1", Memory: "1GiB", Processes: 1, RootDisk: "1GiB"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if stamped.Config[resetGenerationKey] != strconv.FormatInt(3, 10) {
		t.Fatalf("reset rebuild project carries no stamp: %#v", stamped.Config)
	}
	plainExpected, err := (&Client{config: testConfig()}).environmentProject(ProvisionNodeEnvironmentRequest{
		EnvironmentUID: "environment-uid", Revision: "revision",
		Identity:  NodeEnvironmentIdentity{Project: "bf-p-test", Nodes: []NodeIdentity{{LogicalName: "host"}}},
		Resources: NodeEnvironmentResources{CPU: "1", Memory: "1GiB", Processes: 1, RootDisk: "1GiB"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plainExpected.Config[resetGenerationKey] != "" {
		t.Fatalf("plain provision stamped a reset generation: %#v", plainExpected.Config)
	}
	current := &api.Project{ProjectPut: api.ProjectPut{Description: stamped.Description, Config: stamped.Config}, Name: stamped.Name}
	if err := validateEnvironmentProject(current, plainExpected, ProvisionNodeEnvironmentRequest{Identity: reset.Identity}); err != nil {
		t.Fatalf("stamped project failed plain policy validation: %v", err)
	}
}

func TestEnvironmentNetworkUsesConfiguredGatewayCIDR(t *testing.T) {
	request := ProvisionNodeEnvironmentRequest{
		EnvironmentUID:        "environment-550e8400-e29b-41d4-a716-446655440000",
		Revision:              "chrev-aaaaaaaaaaaaaaaa",
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
