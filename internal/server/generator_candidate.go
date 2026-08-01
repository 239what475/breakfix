package server

import (
	"fmt"

	"github.com/breakfix/breakfix/internal/adapter/incus"
	"github.com/breakfix/breakfix/internal/challenge"
	"github.com/breakfix/breakfix/internal/config"
	"github.com/breakfix/breakfix/internal/domain/generation"
)

func candidateExecutionSnapshot(entry challenge.Entry, runtime config.RuntimeConfig, incus incus.Config) (generation.ExecutionSnapshot, error) {
	checkpoints := make([]generation.CheckpointSnapshot, 0, len(entry.Checkpoints))
	for _, checkpoint := range entry.Checkpoints {
		checkpoints = append(checkpoints, generation.CheckpointSnapshot{ID: checkpoint.ID, Node: checkpoint.Node})
	}
	snapshot := generation.ExecutionSnapshot{Runtime: entry.Runtime, Checkpoints: checkpoints}
	switch entry.Runtime {
	case challenge.RuntimeNode:
		if len(entry.Nodes) > incus.MaxNodesPerEnvironment {
			return generation.ExecutionSnapshot{}, fmt.Errorf("node candidate declares %d nodes; platform limit is %d", len(entry.Nodes), incus.MaxNodesPerEnvironment)
		}
		nodes := make([]generation.NodeSnapshot, 0, len(entry.Nodes))
		for _, node := range entry.Nodes {
			nodes = append(nodes, generation.NodeSnapshot{Name: node.Name, Title: node.Title})
		}
		snapshot.Node = &generation.NodeRuntimeSnapshot{
			BaseImageFingerprint:  incus.BaseImageFingerprint,
			ProfileRevision:       runtime.Node.ProfileRevision,
			NetworkPolicyRevision: runtime.Node.NetworkPolicyRevision,
			Nodes:                 nodes,
			Resources: generation.NodeResources{
				CPU: incus.NodeCPU, Memory: incus.NodeMemory, Processes: incus.NodeProcesses, RootDisk: incus.NodeRootDisk,
			},
		}
	case challenge.RuntimeK8s:
		resources := runtime.K8s.Resources
		snapshot.K8s = &generation.K8sRuntimeSnapshot{
			BaseImageDigest:         runtime.K8s.BaseImageDigest,
			ProfileRevision:         runtime.K8s.ProfileRevision,
			Version:                 runtime.K8s.Version,
			ManagementTerminalImage: runtime.K8s.ManagementTerminalImage,
			Resources: generation.K8sResources{
				ControlPlaneCPU: resources.ControlPlaneCPU, ControlPlaneMemory: resources.ControlPlaneMemory,
				ControlPlaneEphemeralStorage: resources.ControlPlaneEphemeralStorage,
				WorkloadCPU:                  resources.WorkloadCPU, WorkloadMemory: resources.WorkloadMemory,
				WorkloadEphemeralStorage: resources.WorkloadEphemeralStorage,
				QuotaCPU:                 resources.QuotaCPU, QuotaMemory: resources.QuotaMemory,
				QuotaEphemeralStorage: resources.QuotaEphemeralStorage,
			},
		}
	default:
		return generation.ExecutionSnapshot{}, fmt.Errorf("unsupported candidate runtime %q", entry.Runtime)
	}
	if err := snapshot.Validate(); err != nil {
		return generation.ExecutionSnapshot{}, fmt.Errorf("freeze candidate execution snapshot: %w", err)
	}
	return snapshot, nil
}
