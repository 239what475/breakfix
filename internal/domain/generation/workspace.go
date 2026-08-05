package generation

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrWorkspaceNotFound = errors.New("generator workspace not found")

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
	State             WorkspaceState
	ProvisionDeadline time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
	DeletedAt         *time.Time
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
	return nil
}
