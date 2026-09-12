// Package execution freezes platform-owned runtime profiles for portable
// scenario content. It does not know whether the caller is authoring or a
// Catalog Release installer.
package execution

import (
	"fmt"

	"github.com/breakfix/breakfix/internal/content/scenario"
	"github.com/breakfix/breakfix/internal/domain/environment"
	domain "github.com/breakfix/breakfix/internal/domain/execution"
)

type SnapshotConfig struct {
	MaxNodes int
	Node     NodeRuntimeConfig
	K8s      K8sRuntimeConfig
}

type NodeRuntimeConfig struct {
	BaseImageFingerprint  string
	ProfileRevision       string
	NetworkPolicyRevision string
	CPU                   string
	Memory                string
	Processes             int64
	RootDisk              string
}

type K8sRuntimeConfig struct {
	BaseImageDigest         string
	ProfileRevision         string
	Version                 string
	ManagementTerminalImage string
	Resources               domain.K8sResources
	Network                 environment.VK8sNetwork
}

// Freeze creates the complete runtime contract before any external build
// starts. Later configuration changes cannot alter this execution.
func Freeze(entry scenario.Entry, config SnapshotConfig) (domain.Snapshot, error) {
	reproduction := make([]domain.ReproductionEvidenceSnapshot, 0, len(entry.Reproduction.Evidence))
	for _, evidence := range entry.Reproduction.Evidence {
		reproduction = append(reproduction, domain.ReproductionEvidenceSnapshot{ID: evidence.ID, Node: evidence.Node})
	}
	checkpoints := make([]domain.CheckpointSnapshot, 0, len(entry.Checkpoints))
	for _, checkpoint := range entry.Checkpoints {
		checkpoints = append(checkpoints, domain.CheckpointSnapshot{ID: checkpoint.ID, Node: checkpoint.Node})
	}
	snapshot := domain.Snapshot{Runtime: entry.Runtime, Reproduction: reproduction, Checkpoints: checkpoints}
	switch entry.Runtime {
	case scenario.RuntimeNode:
		if config.MaxNodes <= 0 || len(entry.Nodes) > config.MaxNodes {
			return domain.Snapshot{}, fmt.Errorf("node candidate declares %d nodes; platform limit is %d", len(entry.Nodes), config.MaxNodes)
		}
		nodes := make([]domain.NodeSnapshot, 0, len(entry.Nodes))
		for _, node := range entry.Nodes {
			nodes = append(nodes, domain.NodeSnapshot{Name: node.Name, Title: node.Title})
		}
		snapshot.Node = &domain.NodeRuntimeSnapshot{
			BaseImageFingerprint:  config.Node.BaseImageFingerprint,
			ProfileRevision:       config.Node.ProfileRevision,
			NetworkPolicyRevision: config.Node.NetworkPolicyRevision,
			Nodes:                 nodes,
			Resources: domain.NodeResources{
				CPU: config.Node.CPU, Memory: config.Node.Memory, Processes: config.Node.Processes, RootDisk: config.Node.RootDisk,
			},
		}
	case scenario.RuntimeK8s:
		snapshot.K8s = &domain.K8sRuntimeSnapshot{
			BaseImageDigest:         config.K8s.BaseImageDigest,
			ProfileRevision:         config.K8s.ProfileRevision,
			Version:                 config.K8s.Version,
			ManagementTerminalImage: config.K8s.ManagementTerminalImage,
			Resources:               config.K8s.Resources,
			Network:                 config.K8s.Network,
		}
	default:
		return domain.Snapshot{}, fmt.Errorf("unsupported candidate runtime %q", entry.Runtime)
	}
	if err := snapshot.Validate(); err != nil {
		return domain.Snapshot{}, fmt.Errorf("freeze candidate execution snapshot: %w", err)
	}
	return snapshot, nil
}
