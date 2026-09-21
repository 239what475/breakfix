package controller

import (
	"errors"
	"fmt"

	"github.com/breakfix/breakfix/internal/bootstrap/config"
	"github.com/breakfix/breakfix/internal/domain/runnable"
	"k8s.io/apimachinery/pkg/api/resource"
)

// Blank lifecycle mirrors the documentation practice environment values:
// hands-on sessions are short-lived, idle sessions are reaped quickly, and the
// maximum lifetime bounds every session even when the terminal stays active.
const (
	blankActionTimeoutSeconds int64 = 900
	blankCreateTimeoutSeconds int64 = 1800
	blankResetTimeoutSeconds  int64 = 900
	blankStopTimeoutSeconds   int64 = 300
	blankReapTimeoutSeconds   int64 = 300
	blankIdleTTLSeconds       int64 = 900
	blankMaxLifetimeSeconds   int64 = 1800
)

// blankPlans resolves installed runtime definitions for blank environments
// from controller configuration. Only the K8s provider is installed today.
type blankPlans struct {
	cfg config.Config
}

func (p blankPlans) BlankPlan(provider string) (runnable.BlankRuntimePlan, error) {
	switch provider {
	case "k8s":
		return p.k8s()
	default:
		return runnable.BlankRuntimePlan{}, fmt.Errorf("blank runtime %q is not installed", provider)
	}
}

func (p blankPlans) k8s() (runnable.BlankRuntimePlan, error) {
	cfg := p.cfg.Runtime.K8s
	memory, err := quantityValue(cfg.Resources.WorkloadMemory)
	if err != nil {
		return runnable.BlankRuntimePlan{}, fmt.Errorf("parse blank workload memory: %w", err)
	}
	disk, err := quantityValue(cfg.Resources.WorkloadEphemeralStorage)
	if err != nil {
		return runnable.BlankRuntimePlan{}, fmt.Errorf("parse blank workload storage: %w", err)
	}
	resources := runnable.ResourceLimits{
		CPU: cfg.Resources.WorkloadCPU, MemoryBytes: memory, EphemeralBytes: disk,
		MaxProcesses: 256, MaxConcurrentTasks: 1,
	}
	target := runnable.TargetLocation{Kind: "management", ID: "cluster"}
	profile := runnable.RuntimeProfile{
		Runtime: runnable.RuntimeK8s, ProfileRevision: cfg.ProfileRevision,
		BaseImage:        cfg.ManagementTerminalImage,
		SoftwareVersions: map[string]string{"kubernetes": cfg.Version},
		Resources:        resources, Network: runnable.NetworkIsolated, Topology: "single-kubernetes-cluster",
		ExecutionBoundaries: []runnable.ExecutionBoundary{
			{ID: "management-write", Target: target, Permission: runnable.PermissionReadWrite, Network: runnable.NetworkIsolated, MaxTimeout: blankActionTimeoutSeconds},
			{ID: "management-read", Target: target, Permission: runnable.PermissionReadOnly, Network: runnable.NetworkIsolated, MaxTimeout: blankActionTimeoutSeconds},
		},
	}
	lifecycle := runnable.LifecyclePolicy{
		CreateTimeoutSeconds: blankCreateTimeoutSeconds,
		ResetTimeoutSeconds:  blankResetTimeoutSeconds,
		StopTimeoutSeconds:   blankStopTimeoutSeconds,
		ReapTimeoutSeconds:   blankReapTimeoutSeconds,
		IdleTTLSeconds:       blankIdleTTLSeconds,
		MaxLifetimeSeconds:   blankMaxLifetimeSeconds,
	}
	plan := runnable.BlankRuntimePlan{Profile: profile, Lifecycle: lifecycle, Image: cfg.ManagementTerminalImage}
	if err := plan.Validate(); err != nil {
		return runnable.BlankRuntimePlan{}, errors.New("blank K8s runtime plan is invalid")
	}
	return plan, nil
}

func quantityValue(value string) (int64, error) {
	quantity, err := resource.ParseQuantity(value)
	if err != nil || quantity.Sign() <= 0 {
		if err == nil {
			err = fmt.Errorf("quantity must be positive")
		}
		return 0, err
	}
	return quantity.Value(), nil
}
