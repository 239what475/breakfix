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

// Workspace is the durable identity and lifecycle of one Generator Run's
// sandbox workspace. Provider implementations only realize this record.
type Workspace struct {
	GeneratorRunID    string
	Namespace         string
	PVCName           string
	SandboxID         string
	State             WorkspaceState
	ProvisionDeadline time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
	DeletedAt         *time.Time
}

// NewPVCName derives a stable Kubernetes-safe PVC name from the opaque Agent
// Run ID. The ID is intentionally never exposed to the model.
func NewWorkspacePVCName(generatorRunID string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(generatorRunID)))
	return "breakfix-workspace-" + hex.EncodeToString(sum[:16])
}

func ValidateWorkspace(record Workspace) error {
	if strings.TrimSpace(record.GeneratorRunID) == "" || strings.TrimSpace(record.Namespace) == "" || strings.TrimSpace(record.PVCName) == "" {
		return errors.New("generator workspace requires run id, namespace, and pvc name")
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
