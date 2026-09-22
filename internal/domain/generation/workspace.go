package generation

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrWorkspaceNotFound        = errors.New("generator workspace not found")
	ErrWorkspaceBusy            = errors.New("generator workspace is already bound to another turn")
	ErrWorkspaceTurnLost        = errors.New("generator workspace turn binding was lost")
	ErrWorkspaceSnapshotCurrent = errors.New("generator workspace already has a current snapshot")
	ErrWorkspaceNotIdle         = errors.New("generator workspace is not eligible for idle retirement")
)

const workspaceSnapshotHolderPrefix = "workspace-snapshot-"

type WorkspaceState string

const (
	WorkspacePending  WorkspaceState = "pending"
	WorkspaceActive   WorkspaceState = "active"
	WorkspaceDeleting WorkspaceState = "deleting"
	WorkspaceDeleted  WorkspaceState = "deleted"
)

// Workspace is the durable identity and lifecycle of one GenerationWorkflow's
// sandbox workspace. A normal AgentRun retry or content repair reuses it. A
// Server interruption retires the record and creates a replacement workspace.
type Workspace struct {
	ID                string
	WorkflowID        string
	Namespace         string
	PVCName           string
	SandboxID         string
	ActiveTurnID      string
	State             WorkspaceState
	ProvisionDeadline time.Time
	IdleSince         *time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
	DeletedAt         *time.Time
}

// WorkspaceOwner projects the cluster-side ownership facts of one Generator
// workspace CR. The database row caches these facts; after a schema reset the
// CR is what a rebuild adopts, never the reverse.
type WorkspaceOwner struct {
	ID         string
	WorkflowID string
	Namespace  string
	PVCName    string
	SandboxID  string
	State      WorkspaceState
	// Terminating reports that the CR carries a deletion timestamp, so its
	// finalizer must be resolved rather than its facts reconciled.
	Terminating bool
}

// Rebuildable reports whether the owner carries the full fact set the
// projection row rebuild requires.
func (o WorkspaceOwner) Rebuildable() bool {
	return strings.TrimSpace(o.ID) != "" && strings.TrimSpace(o.WorkflowID) != "" &&
		strings.TrimSpace(o.Namespace) != "" && strings.TrimSpace(o.PVCName) != ""
}

// WorkspaceSnapshotTarget joins one active workspace with the latest durable
// workflow snapshot pointer. The archive bytes remain Server-private.
type WorkspaceSnapshotTarget struct {
	Workspace      Workspace
	SnapshotDigest string
}

// WorkspaceSnapshotReference protects one immutable archive from background
// garbage collection.
type WorkspaceSnapshotReference struct {
	WorkflowID string
	Digest     string
}

// WorkspaceTurn identifies one explicit Generator client turn. Web authoring
// uses its Authoring AgentRun ID; an MCP client supplies an opaque turn ID.
// The workspace record, rather than an in-memory client connection, owns the
// single-writer fence.
type WorkspaceTurn struct {
	WorkflowID string `json:"workflow_id"`
	ID         string `json:"id"`
}

// NewWorkspaceSnapshotHolderID identifies the Server-only turn that protects
// a workspace while its current contents are archived. It is distinct from a
// user turn so an interactive request can wait for this short-lived operation
// without waiting behind another interactive writer.
func NewWorkspaceSnapshotHolderID() string {
	return NewID("workspace-snapshot")
}

func IsWorkspaceSnapshotHolder(value string) bool {
	return strings.HasPrefix(strings.TrimSpace(value), workspaceSnapshotHolderPrefix)
}

func (t WorkspaceTurn) Valid() bool {
	return strings.TrimSpace(t.WorkflowID) != "" && strings.TrimSpace(t.ID) != ""
}

// WorkspaceFile is the safe, relative projection of one workspace entry.
// Provider paths, ownership, and sandbox identities never leave the Server.
type WorkspaceFile struct {
	Path      string `json:"path"`
	Directory bool   `json:"directory"`
	Size      int64  `json:"size"`
}

// NewWorkspacePVCName derives a stable Kubernetes-safe PVC name from the
// opaque workspace identity. It intentionally does not use the workflow ID:
// an interrupted workflow needs a new PVC while the old one is reaped.
func NewWorkspacePVCName(workspaceID string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(workspaceID)))
	return "breakfix-workspace-" + hex.EncodeToString(sum[:16])
}

func ValidateWorkspace(record Workspace) error {
	if strings.TrimSpace(record.ID) == "" || strings.TrimSpace(record.WorkflowID) == "" || strings.TrimSpace(record.Namespace) == "" || strings.TrimSpace(record.PVCName) == "" {
		return errors.New("generator workspace requires id, workflow id, namespace, and pvc name")
	}
	if record.State == "" {
		record.State = WorkspacePending
	}
	switch record.State {
	case WorkspacePending, WorkspaceActive, WorkspaceDeleting, WorkspaceDeleted:
	default:
		return fmt.Errorf("invalid generator workspace state %q", record.State)
	}
	if record.ProvisionDeadline.IsZero() {
		return errors.New("generator workspace provision deadline is required")
	}
	if record.IdleSince != nil && (record.State != WorkspaceActive || strings.TrimSpace(record.ActiveTurnID) != "") {
		return errors.New("generator workspace idle timestamp requires an unbound active workspace")
	}
	return nil
}
