package runtimeprofile

import "testing"

func TestVK8sResourcesValidate(t *testing.T) {
	resources := VK8sResources{
		ControlPlaneCPU: "500m", ControlPlaneMemory: "512Mi", ControlPlaneEphemeralStorage: "10Gi",
		WorkloadCPU: "500m", WorkloadMemory: "512Mi", WorkloadEphemeralStorage: "3Gi",
		QuotaCPU: "3", QuotaMemory: "3Gi", QuotaEphemeralStorage: "30Gi",
	}
	if err := resources.Validate(); err != nil {
		t.Fatalf("validate resources: %v", err)
	}
}

func TestVK8sResourcesRejectsQuotaBelowPlatformBaseline(t *testing.T) {
	resources := VK8sResources{
		ControlPlaneCPU: "500m", ControlPlaneMemory: "512Mi", ControlPlaneEphemeralStorage: "10Gi",
		WorkloadCPU: "500m", WorkloadMemory: "512Mi", WorkloadEphemeralStorage: "3Gi",
		QuotaCPU: "1", QuotaMemory: "3Gi", QuotaEphemeralStorage: "30Gi",
	}
	if err := resources.Validate(); err == nil {
		t.Fatal("validate resources unexpectedly accepted a quota below the platform baseline")
	}
}
