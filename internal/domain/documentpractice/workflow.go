package documentpractice

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type AgentAudit struct {
	RunID         string    `json:"run_id"`
	Role          string    `json:"role"`
	Model         string    `json:"model"`
	PromptVersion string    `json:"prompt_version"`
	ToolVersion   string    `json:"tool_version"`
	PolicyVersion string    `json:"policy_version"`
	InputDigest   string    `json:"input_digest"`
	OutputDigest  string    `json:"output_digest"`
	CreatedAt     time.Time `json:"created_at"`
}

func (a AgentAudit) Validate() error {
	if strings.TrimSpace(a.RunID) == "" || strings.TrimSpace(a.Role) == "" || strings.TrimSpace(a.Model) == "" || strings.TrimSpace(a.PromptVersion) == "" || strings.TrimSpace(a.ToolVersion) == "" || strings.TrimSpace(a.PolicyVersion) == "" || !validDigest(a.InputDigest) || !validDigest(a.OutputDigest) || a.CreatedAt.IsZero() {
		return errors.New("agent audit metadata is incomplete")
	}
	return nil
}

func DigestAgentInput(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

type WorkflowState string

const (
	Planning              WorkflowState = "Planning"
	PlanReviewing         WorkflowState = "PlanReviewing"
	Generating            WorkflowState = "Generating"
	ArtifactReviewing     WorkflowState = "ArtifactReviewing"
	MaterializingArtifact WorkflowState = "MaterializingArtifact"
	Verifying             WorkflowState = "Verifying"
	VerificationReviewing WorkflowState = "VerificationReviewing"
	Publishing            WorkflowState = "Publishing"
	Published             WorkflowState = "Published"
	NoPractice            WorkflowState = "NoPractice"
	Rejected              WorkflowState = "Rejected"
	Failed                WorkflowState = "Failed"
)

func (s WorkflowState) Terminal() bool {
	return s == Published || s == NoPractice || s == Rejected || s == Failed
}

type ArtifactRecord struct {
	ID              string    `json:"id"`
	Kind            string    `json:"kind"`
	ParentID        string    `json:"parent_id,omitempty"`
	ContentRevision string    `json:"content_revision"`
	Digest          string    `json:"digest"`
	SchemaVersion   string    `json:"schema_version"`
	OwnerRole       string    `json:"owner_role"`
	PolicyVersion   string    `json:"policy_version,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	Payload         []byte    `json:"payload,omitempty"`
}

func (a ArtifactRecord) Validate() error {
	if strings.TrimSpace(a.ID) == "" || strings.TrimSpace(a.Kind) == "" || strings.TrimSpace(a.ContentRevision) == "" || strings.TrimSpace(a.SchemaVersion) == "" || strings.TrimSpace(a.OwnerRole) == "" || !validDigest(a.Digest) || a.CreatedAt.IsZero() {
		return errors.New("artifact record is incomplete")
	}
	return nil
}

type Workflow struct {
	ID             string           `json:"id"`
	State          WorkflowState    `json:"state"`
	StateVersion   int64            `json:"state_version"`
	Revision       int64            `json:"revision"`
	MaxRevisions   int64            `json:"max_revisions"`
	LeaseOwner     string           `json:"lease_owner,omitempty"`
	LeaseExpiresAt *time.Time       `json:"lease_expires_at,omitempty"`
	Artifacts      []ArtifactRecord `json:"artifacts"`
	UpdatedAt      time.Time        `json:"updated_at"`
}

func NewWorkflow(id string, now time.Time) (Workflow, error) {
	if strings.TrimSpace(id) == "" || now.IsZero() {
		return Workflow{}, errors.New("workflow requires id and creation time")
	}
	return Workflow{ID: id, State: Planning, StateVersion: 1, Revision: 1, MaxRevisions: 3, UpdatedAt: now.UTC(), Artifacts: []ArtifactRecord{}}, nil
}

func (w Workflow) Validate() error {
	if strings.TrimSpace(w.ID) == "" || w.StateVersion < 1 || w.Revision < 1 || w.MaxRevisions < 1 || !validState(w.State) || w.UpdatedAt.IsZero() {
		return errors.New("workflow is invalid")
	}
	for _, a := range w.Artifacts {
		if err := a.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func validState(s WorkflowState) bool {
	switch s {
	case Planning, PlanReviewing, Generating, ArtifactReviewing, MaterializingArtifact, Verifying, VerificationReviewing, Publishing, Published, NoPractice, Rejected, Failed:
		return true
	}
	return false
}

// Append refuses mutation of an existing artifact ID or digest. The ledger is
// append-only even when the workflow state itself advances.
func (w *Workflow) Append(a ArtifactRecord, now time.Time) error {
	if w == nil {
		return errors.New("workflow is nil")
	}
	if err := a.Validate(); err != nil {
		return err
	}
	for _, old := range w.Artifacts {
		if old.ID == a.ID {
			if old.Digest == a.Digest {
				return nil
			}
			return errors.New("artifact id already exists with another digest")
		}
	}
	w.Artifacts = append(w.Artifacts, a)
	w.UpdatedAt = now.UTC()
	return nil
}

func (w *Workflow) Advance(next WorkflowState, requiredKinds ...string) error {
	return w.AdvanceAt(next, time.Now().UTC(), requiredKinds...)
}

func (w *Workflow) AdvanceAt(next WorkflowState, now time.Time, requiredKinds ...string) error {
	if w == nil || w.State.Terminal() {
		return errors.New("workflow is terminal")
	}
	if !allowedTransition(w.State, next) {
		return fmt.Errorf("invalid workflow transition %s -> %s", w.State, next)
	}
	for _, kind := range requiredKinds {
		found := false
		for _, a := range w.Artifacts {
			if a.Kind == kind {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("workflow transition requires artifact %q", kind)
		}
	}
	if now.IsZero() {
		return errors.New("workflow transition time is required")
	}
	w.State = next
	w.StateVersion++
	w.UpdatedAt = now.UTC()
	return nil
}

func (w *Workflow) Revise() error {
	return w.ReviseAt(time.Now().UTC())
}

func (w *Workflow) ReviseAt(now time.Time) error {
	if w == nil || w.State.Terminal() {
		return errors.New("workflow cannot be revised")
	}
	if w.Revision >= w.MaxRevisions {
		return errors.New("workflow revision limit reached")
	}
	w.Revision++
	w.State = Planning
	w.StateVersion++
	if now.IsZero() {
		return errors.New("workflow revision time is required")
	}
	w.UpdatedAt = now.UTC()
	return nil
}

func allowedTransition(from, to WorkflowState) bool {
	switch from {
	case Planning:
		return to == PlanReviewing || to == Rejected || to == Failed
	case PlanReviewing:
		return to == Generating || to == NoPractice || to == Rejected || to == Failed
	case Generating:
		return to == ArtifactReviewing || to == Failed
	case ArtifactReviewing:
		return to == MaterializingArtifact || to == Generating || to == Rejected || to == Failed
	case MaterializingArtifact:
		return to == Verifying || to == Failed
	case Verifying:
		return to == VerificationReviewing || to == Failed
	case VerificationReviewing:
		return to == Publishing || to == Failed || to == Verifying
	case Publishing:
		return to == Published || to == Failed
	}
	return false
}

func validDigest(value string) bool { return len(value) == 71 && strings.HasPrefix(value, "sha256:") }
