package server

import (
	"fmt"

	"github.com/breakfix/breakfix/internal/candidate"
	"github.com/breakfix/breakfix/internal/challenge"
	"github.com/breakfix/breakfix/internal/config"
	"github.com/breakfix/breakfix/internal/incusprovider"
)

func candidateExecutionSnapshot(entry challenge.Entry, runtime config.RuntimeConfig, incus incusprovider.Config) (candidate.ExecutionSnapshot, error) {
	checkpoints := make([]candidate.CheckpointSnapshot, 0, len(entry.Checkpoints))
	for _, checkpoint := range entry.Checkpoints {
		checkpoints = append(checkpoints, candidate.CheckpointSnapshot{ID: checkpoint.ID, Node: checkpoint.Node})
	}
	snapshot := candidate.ExecutionSnapshot{Runtime: entry.Runtime, Checkpoints: checkpoints}
	switch entry.Runtime {
	case challenge.RuntimeNode:
		if len(entry.Nodes) > incus.MaxNodesPerEnvironment {
			return candidate.ExecutionSnapshot{}, fmt.Errorf("node candidate declares %d nodes; platform limit is %d", len(entry.Nodes), incus.MaxNodesPerEnvironment)
		}
		nodes := make([]candidate.NodeSnapshot, 0, len(entry.Nodes))
		for _, node := range entry.Nodes {
			nodes = append(nodes, candidate.NodeSnapshot{Name: node.Name, Title: node.Title})
		}
		snapshot.Node = &candidate.NodeRuntimeSnapshot{
			BaseImageFingerprint:  incus.BaseImageFingerprint,
			ProfileRevision:       runtime.Node.ProfileRevision,
			NetworkPolicyRevision: runtime.Node.NetworkPolicyRevision,
			Nodes:                 nodes,
			Resources: candidate.NodeResources{
				CPU: incus.NodeCPU, Memory: incus.NodeMemory, Processes: incus.NodeProcesses, RootDisk: incus.NodeRootDisk,
			},
		}
	case challenge.RuntimeK8s:
		resources := runtime.K8s.Resources
		snapshot.K8s = &candidate.K8sRuntimeSnapshot{
			BaseImageDigest:         runtime.K8s.BaseImageDigest,
			ProfileRevision:         runtime.K8s.ProfileRevision,
			Version:                 runtime.K8s.Version,
			ManagementTerminalImage: runtime.K8s.ManagementTerminalImage,
			Resources: candidate.K8sResources{
				ControlPlaneCPU: resources.ControlPlaneCPU, ControlPlaneMemory: resources.ControlPlaneMemory,
				ControlPlaneEphemeralStorage: resources.ControlPlaneEphemeralStorage,
				WorkloadCPU:                  resources.WorkloadCPU, WorkloadMemory: resources.WorkloadMemory,
				WorkloadEphemeralStorage: resources.WorkloadEphemeralStorage,
				QuotaCPU:                 resources.QuotaCPU, QuotaMemory: resources.QuotaMemory,
				QuotaEphemeralStorage: resources.QuotaEphemeralStorage,
			},
		}
	default:
		return candidate.ExecutionSnapshot{}, fmt.Errorf("unsupported candidate runtime %q", entry.Runtime)
	}
	if err := snapshot.Validate(); err != nil {
		return candidate.ExecutionSnapshot{}, fmt.Errorf("freeze candidate execution snapshot: %w", err)
	}
	return snapshot, nil
}
