package environment

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
)

// VK8sResources is the immutable resource profile copied into each VK8s
// environment. Workload resources are applied to the management terminal and
// to the vcluster LimitRange for containers that do not declare resources.
type VK8sResources struct {
	ControlPlaneCPU              string
	ControlPlaneMemory           string
	ControlPlaneEphemeralStorage string
	WorkloadCPU                  string
	WorkloadMemory               string
	WorkloadEphemeralStorage     string
	QuotaCPU                     string
	QuotaMemory                  string
	QuotaEphemeralStorage        string
}

// Validate verifies quantities and ensures the environment quota can start
// the platform itself: the vcluster control plane, its distro init container,
// and the management environment. Remaining quota is available to challenge
// workloads.
func (r VK8sResources) Validate() error {
	values := map[string]string{
		"control_plane_cpu":               r.ControlPlaneCPU,
		"control_plane_memory":            r.ControlPlaneMemory,
		"control_plane_ephemeral_storage": r.ControlPlaneEphemeralStorage,
		"workload_cpu":                    r.WorkloadCPU,
		"workload_memory":                 r.WorkloadMemory,
		"workload_ephemeral_storage":      r.WorkloadEphemeralStorage,
		"quota_cpu":                       r.QuotaCPU,
		"quota_memory":                    r.QuotaMemory,
		"quota_ephemeral_storage":         r.QuotaEphemeralStorage,
	}
	parsed := make(map[string]resource.Quantity, len(values))
	for field, value := range values {
		quantity, err := resource.ParseQuantity(strings.TrimSpace(value))
		if err != nil || quantity.Sign() <= 0 {
			return fmt.Errorf("%s must be a positive Kubernetes quantity", field)
		}
		parsed[field] = quantity
	}
	for _, budget := range []struct {
		name  string
		quota resource.Quantity
		base  resource.Quantity
	}{
		{name: "cpu", quota: parsed["quota_cpu"], base: platformBaseline(parsed["control_plane_cpu"], parsed["workload_cpu"])},
		{name: "memory", quota: parsed["quota_memory"], base: platformBaseline(parsed["control_plane_memory"], parsed["workload_memory"])},
		{name: "ephemeral_storage", quota: parsed["quota_ephemeral_storage"], base: platformBaseline(parsed["control_plane_ephemeral_storage"], parsed["workload_ephemeral_storage"])},
	} {
		if budget.quota.Cmp(budget.base) < 0 {
			return fmt.Errorf("quota_%s must cover the platform baseline of %s", budget.name, budget.base.String())
		}
	}
	return nil
}

func platformBaseline(controlPlane, workload resource.Quantity) resource.Quantity {
	baseline := controlPlane
	baseline.Add(workload) // vcluster distro init container receives the LimitRange default.
	baseline.Add(workload) // Breakfix management terminal uses the workload profile.
	return baseline
}
