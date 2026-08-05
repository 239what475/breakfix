package environment

import (
	"fmt"
	"net/netip"
	"strings"
)

// VK8sNetwork is the immutable egress boundary shared by a virtual cluster's
// workload policy and its Breakfix management terminal policy.
type VK8sNetwork struct {
	PublicEgressCIDR string
	ProtectedCIDRs   []string
}

// Validate ensures every protected range can be represented as an IPBlock
// exception beneath the one IPv4 public-egress range supported by vcluster.
func (n VK8sNetwork) Validate() error {
	public, err := parseCanonicalIPv4Prefix("public_egress_cidr", n.PublicEgressCIDR)
	if err != nil {
		return err
	}
	if len(n.ProtectedCIDRs) == 0 {
		return fmt.Errorf("protected_cidrs is required")
	}
	seen := make(map[string]struct{}, len(n.ProtectedCIDRs))
	for index, value := range n.ProtectedCIDRs {
		prefix, err := parseCanonicalIPv4Prefix(fmt.Sprintf("protected_cidrs[%d]", index), value)
		if err != nil {
			return err
		}
		if !public.Contains(prefix.Addr()) || public.Bits() > prefix.Bits() {
			return fmt.Errorf("protected_cidrs[%d] must be contained by public_egress_cidr", index)
		}
		canonical := prefix.String()
		if _, duplicate := seen[canonical]; duplicate {
			return fmt.Errorf("protected_cidrs contains duplicate %q", canonical)
		}
		seen[canonical] = struct{}{}
	}
	return nil
}

func parseCanonicalIPv4Prefix(field, value string) (netip.Prefix, error) {
	prefix, err := netip.ParsePrefix(strings.TrimSpace(value))
	if err != nil || !prefix.Addr().Is4() || prefix != prefix.Masked() {
		return netip.Prefix{}, fmt.Errorf("%s must be a canonical IPv4 CIDR", field)
	}
	return prefix, nil
}
