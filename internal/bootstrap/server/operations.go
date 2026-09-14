package server

import (
	"fmt"

	appoperations "github.com/breakfix/breakfix/internal/application/operations"
	"github.com/breakfix/breakfix/internal/bootstrap/config"
	"github.com/breakfix/breakfix/internal/domain/runnable"
	"github.com/docker/go-units"
	"k8s.io/apimachinery/pkg/api/resource"
)

const (
	operationsActionTimeoutSeconds int64 = 900
	operationsCreateTimeoutSeconds int64 = 1800
	operationsResetTimeoutSeconds  int64 = 900
	operationsStopTimeoutSeconds   int64 = 300
	operationsReapTimeoutSeconds   int64 = 300
	operationsIdleTTLSeconds       int64 = 1800
	operationsMaxLifetimeSeconds   int64 = 3600
)

func operationsRuntimeConfig(cfg config.Config) (appoperations.Config, error) {
	nodeMemory, err := units.RAMInBytes(cfg.Incus.NodeMemory)
	if err != nil || nodeMemory <= 0 {
		if err == nil {
			err = fmt.Errorf("quantity must be positive")
		}
		return appoperations.Config{}, fmt.Errorf("parse Operations Node memory: %w", err)
	}
	nodeDisk, err := units.RAMInBytes(cfg.Incus.NodeRootDisk)
	if err != nil || nodeDisk <= 0 {
		if err == nil {
			err = fmt.Errorf("quantity must be positive")
		}
		return appoperations.Config{}, fmt.Errorf("parse Operations Node disk: %w", err)
	}
	workloadMemory, err := quantityValue(cfg.Runtime.K8s.Resources.WorkloadMemory)
	if err != nil {
		return appoperations.Config{}, fmt.Errorf("parse Operations K8s workload memory: %w", err)
	}
	workloadDisk, err := quantityValue(cfg.Runtime.K8s.Resources.WorkloadEphemeralStorage)
	if err != nil {
		return appoperations.Config{}, fmt.Errorf("parse Operations K8s workload storage: %w", err)
	}
	lifecycle := runnable.LifecyclePolicy{
		CreateTimeoutSeconds: operationsCreateTimeoutSeconds,
		ResetTimeoutSeconds:  operationsResetTimeoutSeconds,
		StopTimeoutSeconds:   operationsStopTimeoutSeconds,
		ReapTimeoutSeconds:   operationsReapTimeoutSeconds,
		IdleTTLSeconds:       operationsIdleTTLSeconds,
		MaxLifetimeSeconds:   operationsMaxLifetimeSeconds,
	}
	return appoperations.Config{
		MaxNodes: cfg.Incus.MaxNodesPerEnvironment,
		Node: appoperations.RuntimeProfileConfig{
			ProfileRevision: cfg.Runtime.Node.ProfileRevision,
			BaseImage:       "incus://" + cfg.Incus.BaseImageFingerprint,
			Resources: runnable.ResourceLimits{
				CPU: cfg.Incus.NodeCPU, MemoryBytes: nodeMemory, EphemeralBytes: nodeDisk,
				MaxProcesses: cfg.Incus.NodeProcesses, MaxConcurrentTasks: 1,
			},
			Network:          runnable.NetworkPrivate,
			MaxActionTimeout: operationsActionTimeoutSeconds,
		},
		K8s: appoperations.RuntimeProfileConfig{
			ProfileRevision: cfg.Runtime.K8s.ProfileRevision,
			BaseImage:       cfg.Runtime.K8s.BaseImageDigest,
			SoftwareVersions: map[string]string{
				"kubernetes": cfg.Runtime.K8s.Version,
			},
			Resources: runnable.ResourceLimits{
				CPU: cfg.Runtime.K8s.Resources.WorkloadCPU, MemoryBytes: workloadMemory, EphemeralBytes: workloadDisk,
				MaxProcesses: 256, MaxConcurrentTasks: 1,
			},
			Network:          runnable.NetworkIsolated,
			MaxActionTimeout: operationsActionTimeoutSeconds,
		},
		Lifecycle: lifecycle,
	}, nil
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
