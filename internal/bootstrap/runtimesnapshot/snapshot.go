// Package runtimesnapshot converts bootstrap configuration into the immutable
// runtime input shared by authoring and Catalog Release execution.
package runtimesnapshot

import (
	"github.com/breakfix/breakfix/internal/adapter/incus"
	appexecution "github.com/breakfix/breakfix/internal/application/execution"
	"github.com/breakfix/breakfix/internal/bootstrap/config"
	domainexecution "github.com/breakfix/breakfix/internal/domain/execution"
)

func From(runtime config.RuntimeConfig, incusConfig incus.Config) appexecution.SnapshotConfig {
	resources := runtime.K8s.Resources
	return appexecution.SnapshotConfig{
		MaxNodes: incusConfig.MaxNodesPerEnvironment,
		Node: appexecution.NodeRuntimeConfig{
			BaseImageFingerprint:  incusConfig.BaseImageFingerprint,
			ProfileRevision:       runtime.Node.ProfileRevision,
			NetworkPolicyRevision: runtime.Node.NetworkPolicyRevision,
			CPU:                   incusConfig.NodeCPU,
			Memory:                incusConfig.NodeMemory,
			Processes:             incusConfig.NodeProcesses,
			RootDisk:              incusConfig.NodeRootDisk,
		},
		K8s: appexecution.K8sRuntimeConfig{
			BaseImageDigest:         runtime.K8s.BaseImageDigest,
			ProfileRevision:         runtime.K8s.ProfileRevision,
			Version:                 runtime.K8s.Version,
			ManagementTerminalImage: runtime.K8s.ManagementTerminalImage,
			Resources: domainexecution.K8sResources{
				ControlPlaneCPU:              resources.ControlPlaneCPU,
				ControlPlaneMemory:           resources.ControlPlaneMemory,
				ControlPlaneEphemeralStorage: resources.ControlPlaneEphemeralStorage,
				WorkloadCPU:                  resources.WorkloadCPU,
				WorkloadMemory:               resources.WorkloadMemory,
				WorkloadEphemeralStorage:     resources.WorkloadEphemeralStorage,
				QuotaCPU:                     resources.QuotaCPU,
				QuotaMemory:                  resources.QuotaMemory,
				QuotaEphemeralStorage:        resources.QuotaEphemeralStorage,
			},
		},
	}
}
