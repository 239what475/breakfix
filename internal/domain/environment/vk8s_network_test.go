package environment

import "testing"

func TestVK8sNetworkValidate(t *testing.T) {
	tests := []struct {
		name    string
		network VK8sNetwork
		valid   bool
	}{
		{
			name: "configured public boundary with protected ranges",
			network: VK8sNetwork{
				PublicEgressCIDR: "0.0.0.0/0",
				ProtectedCIDRs:   []string{"10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.168.0.0/16"},
			},
			valid: true,
		},
		{
			name: "missing protected boundary",
			network: VK8sNetwork{
				PublicEgressCIDR: "0.0.0.0/0",
			},
		},
		{
			name: "non canonical CIDR",
			network: VK8sNetwork{
				PublicEgressCIDR: "0.0.0.1/0",
				ProtectedCIDRs:   []string{"10.0.0.0/8"},
			},
		},
		{
			name: "protected range outside public boundary",
			network: VK8sNetwork{
				PublicEgressCIDR: "203.0.113.0/24",
				ProtectedCIDRs:   []string{"10.0.0.0/8"},
			},
		},
		{
			name: "duplicate protected range",
			network: VK8sNetwork{
				PublicEgressCIDR: "0.0.0.0/0",
				ProtectedCIDRs:   []string{"10.0.0.0/8", "10.0.0.0/8"},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.network.Validate()
			if test.valid && err != nil {
				t.Fatalf("validate network: %v", err)
			}
			if !test.valid && err == nil {
				t.Fatal("validate network unexpectedly succeeded")
			}
		})
	}
}
