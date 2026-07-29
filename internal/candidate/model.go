// Package candidate owns immutable generated challenge revisions and the
// domain state that moves them through build, artifact publication, real
// verification, author review, and catalog publication.
package candidate

import (
	"errors"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/challenge"
)

var ErrNotFound = errors.New("candidate revision not found")

type State string

const (
	StateBuilding             State = "Building"
	StatePublishingArtifact   State = "PublishingArtifact"
	StateVerifying            State = "Verifying"
	StateVerified             State = "Verified"
	StatePublishingChallenge  State = "PublishingChallenge"
	StatePublished            State = "Published"
	StateArtifactFailed       State = "ArtifactFailed"
	StateInfrastructureFailed State = "InfrastructureFailed"
	StateCancelled            State = "Cancelled"
	StateSuperseded           State = "Superseded"
)

type FailureClass string

const (
	FailureArtifact       FailureClass = "artifact"
	FailureInfrastructure FailureClass = "infrastructure"
	FailureCancelled      FailureClass = "cancelled"
)

type CheckpointSnapshot struct {
	ID   string `json:"id"`
	Node string `json:"node,omitempty"`
}

type NodeSnapshot struct {
	Name  string `json:"name"`
	Title string `json:"title"`
}

type NodeRuntimeSnapshot struct {
	BaseImageFingerprint  string         `json:"base_image_fingerprint"`
	ProfileRevision       string         `json:"profile_revision"`
	NetworkPolicyRevision string         `json:"network_policy_revision"`
	Nodes                 []NodeSnapshot `json:"nodes"`
}

type K8sRuntimeSnapshot struct {
	BaseImageDigest         string `json:"base_image_digest"`
	ProfileRevision         string `json:"profile_revision"`
	Version                 string `json:"version"`
	ManagementTerminalImage string `json:"management_terminal_image"`
}

type ExecutionSnapshot struct {
	Runtime     string               `json:"runtime"`
	Checkpoints []CheckpointSnapshot `json:"checkpoints"`
	Node        *NodeRuntimeSnapshot `json:"node,omitempty"`
	K8s         *K8sRuntimeSnapshot  `json:"k8s,omitempty"`
}

type BuildOutput struct {
	Runtime           string `json:"runtime"`
	OCIArchivePath    string `json:"oci_archive_path,omitempty"`
	OCIArchiveDigest  string `json:"oci_archive_digest,omitempty"`
	IncusBuildProject string `json:"incus_build_project,omitempty"`
	IncusFingerprint  string `json:"incus_fingerprint,omitempty"`
}

type ArtifactReference struct {
	Runtime          string `json:"runtime"`
	OCIDigest        string `json:"oci_digest,omitempty"`
	IncusFingerprint string `json:"incus_fingerprint,omitempty"`
}

type ExecutionResult struct {
	Location string `json:"location"`
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout,omitempty"`
	Stderr   string `json:"stderr,omitempty"`
}

type CheckpointResult struct {
	ID      string `json:"id"`
	Passed  bool   `json:"passed"`
	Summary string `json:"summary"`
	Details string `json:"details,omitempty"`
}

type VerificationReport struct {
	Passed      bool               `json:"passed"`
	Answers     []ExecutionResult  `json:"answers"`
	Checkpoints []CheckpointResult `json:"checkpoints"`
	Summary     string             `json:"summary,omitempty"`
}

type Failure struct {
	Class   FailureClass `json:"class"`
	Code    string       `json:"code"`
	Summary string       `json:"summary"`
}

type Publication struct {
	ChallengeID string `json:"challenge_id"`
	SourceSlug  string `json:"source_slug"`
	TargetPath  string `json:"target_path"`
}

type Revision struct {
	ID                 string
	AuthoringSessionID string
	AuthoringRevision  int64
	GeneratorSessionID string
	GeneratorRunID     string
	JudgeRunID         string
	ArchivePath        string
	ArchiveSHA256      string
	Snapshot           ExecutionSnapshot
	State              State
	Build              *BuildOutput
	Artifact           *ArtifactReference
	Verification       *VerificationReport
	Failure            *Failure
	Publication        *Publication
	SupersededBy       string
	CreatedAt          time.Time
	UpdatedAt          time.Time
	VerifiedAt         *time.Time
	PublishedAt        *time.Time
}

func (s ExecutionSnapshot) Validate() error {
	if s.Runtime != challenge.RuntimeNode && s.Runtime != challenge.RuntimeK8s {
		return errors.New("candidate snapshot has an invalid runtime")
	}
	if len(s.Checkpoints) == 0 {
		return errors.New("candidate snapshot requires checkpoints")
	}
	seen := make(map[string]struct{}, len(s.Checkpoints))
	for _, checkpoint := range s.Checkpoints {
		if strings.TrimSpace(checkpoint.ID) == "" {
			return errors.New("candidate snapshot has an empty checkpoint id")
		}
		if _, duplicate := seen[checkpoint.ID]; duplicate {
			return errors.New("candidate snapshot has duplicate checkpoint ids")
		}
		seen[checkpoint.ID] = struct{}{}
	}
	if s.Runtime == challenge.RuntimeNode {
		if s.Node == nil || s.K8s != nil || len(s.Node.Nodes) == 0 || strings.TrimSpace(s.Node.BaseImageFingerprint) == "" {
			return errors.New("node candidate snapshot is incomplete")
		}
		nodes := make(map[string]struct{}, len(s.Node.Nodes))
		for _, node := range s.Node.Nodes {
			nodes[node.Name] = struct{}{}
		}
		for _, checkpoint := range s.Checkpoints {
			if _, ok := nodes[checkpoint.Node]; !ok {
				return errors.New("node candidate checkpoint references an unknown node")
			}
		}
	} else if s.K8s == nil || s.Node != nil || strings.TrimSpace(s.K8s.BaseImageDigest) == "" {
		return errors.New("k8s candidate snapshot is incomplete")
	} else {
		for _, checkpoint := range s.Checkpoints {
			if strings.TrimSpace(checkpoint.Node) != "" {
				return errors.New("k8s candidate checkpoint declares a node")
			}
		}
	}
	return nil
}

func Terminal(state State) bool {
	switch state {
	case StatePublished, StateArtifactFailed, StateInfrastructureFailed, StateCancelled, StateSuperseded:
		return true
	default:
		return false
	}
}
