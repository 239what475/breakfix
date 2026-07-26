// Package workspace defines the Server-owned durable record for a Generator
// Sandbox workspace. OpenSandbox only mounts the PVC; it never owns it.
package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrNotFound = errors.New("generator workspace not found")

type State string

const (
	StatePending  State = "pending"
	StateActive   State = "active"
	StateDeleting State = "deleting"
	StateDeleted  State = "deleted"
)

type Record struct {
	GeneratorSessionID string
	Namespace          string
	PVCName            string
	SandboxID          string
	State              State
	ProvisionDeadline  time.Time
	CreatedAt          time.Time
	UpdatedAt          time.Time
	DeletedAt          *time.Time
}

// NewPVCName derives a stable Kubernetes-safe PVC name from the opaque Agent
// Session ID. The ID is intentionally never exposed to the model.
func NewPVCName(generatorSessionID string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(generatorSessionID)))
	return "breakfix-workspace-" + hex.EncodeToString(sum[:16])
}

func Validate(record Record) error {
	if strings.TrimSpace(record.GeneratorSessionID) == "" || strings.TrimSpace(record.Namespace) == "" || strings.TrimSpace(record.PVCName) == "" {
		return errors.New("generator workspace requires session id, namespace, and pvc name")
	}
	if record.State == "" {
		record.State = StatePending
	}
	switch record.State {
	case StatePending, StateActive, StateDeleting, StateDeleted:
	default:
		return fmt.Errorf("invalid generator workspace state %q", record.State)
	}
	if record.ProvisionDeadline.IsZero() {
		return errors.New("generator workspace provision deadline is required")
	}
	return nil
}

type Repository interface {
	CreateGeneratorWorkspace(context.Context, Record) (*Record, error)
	GetGeneratorWorkspace(context.Context, string) (*Record, error)
	ActivateGeneratorWorkspace(context.Context, string, string, time.Time) error
	BeginGeneratorWorkspaceCleanup(context.Context, string, time.Time) (*Record, error)
	MarkGeneratorWorkspaceDeleted(context.Context, string, time.Time) error
	ListExpiredPendingGeneratorWorkspaces(context.Context, time.Time) ([]Record, error)
	ListDeletingGeneratorWorkspaces(context.Context) ([]Record, error)
}
