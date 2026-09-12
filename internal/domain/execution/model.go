// Package execution owns immutable runtime data shared by every scenario
// build and verification path. It deliberately has no workflow state, agent
// lineage, or publication intent.
package execution

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/content/scenario"
	"github.com/breakfix/breakfix/internal/domain/environment"
)

// DefaultActionDeadline bounds one provider-side runtime action. It is derived
// when a worker starts an action and is never persisted on a workflow or
// inherited by a retry.
const DefaultActionDeadline = time.Hour

func NewActionDeadline(now time.Time) time.Time {
	return now.UTC().Add(DefaultActionDeadline)
}

type CheckpointSnapshot struct {
	ID   string `json:"id"`
	Node string `json:"node,omitempty"`
}

// ReproductionEvidenceSnapshot pins one observation that must be true in the
// initialized environment before a reference repair is evaluated.
type ReproductionEvidenceSnapshot struct {
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
	Resources             NodeResources  `json:"resources"`
}

type NodeResources struct {
	CPU       string `json:"cpu"`
	Memory    string `json:"memory"`
	Processes int64  `json:"processes"`
	RootDisk  string `json:"root_disk"`
}

type K8sRuntimeSnapshot struct {
	BaseImageDigest         string                  `json:"base_image_digest"`
	ProfileRevision         string                  `json:"profile_revision"`
	Version                 string                  `json:"version"`
	ManagementTerminalImage string                  `json:"management_terminal_image"`
	Resources               K8sResources            `json:"resources"`
	Network                 environment.VK8sNetwork `json:"network"`
}

type K8sResources struct {
	ControlPlaneCPU              string `json:"control_plane_cpu"`
	ControlPlaneMemory           string `json:"control_plane_memory"`
	ControlPlaneEphemeralStorage string `json:"control_plane_ephemeral_storage"`
	WorkloadCPU                  string `json:"workload_cpu"`
	WorkloadMemory               string `json:"workload_memory"`
	WorkloadEphemeralStorage     string `json:"workload_ephemeral_storage"`
	QuotaCPU                     string `json:"quota_cpu"`
	QuotaMemory                  string `json:"quota_memory"`
	QuotaEphemeralStorage        string `json:"quota_ephemeral_storage"`
}

// Snapshot freezes the platform runtime profile used to build and verify one
// portable scenario. It remains valid independent of its work owner.
type Snapshot struct {
	Runtime      string                         `json:"runtime"`
	Reproduction []ReproductionEvidenceSnapshot `json:"reproduction,omitempty"`
	// ReferenceRepair is nil for snapshots written before learning aids became
	// optional. Those revisions retain the original answer/checkpoint contract.
	ReferenceRepair *bool                `json:"reference_repair,omitempty"`
	Checkpoints     []CheckpointSnapshot `json:"checkpoints"`
	Node            *NodeRuntimeSnapshot `json:"node,omitempty"`
	K8s             *K8sRuntimeSnapshot  `json:"k8s,omitempty"`
}

type BuildOutput struct {
	Runtime      string               `json:"runtime"`
	OCIReference string               `json:"oci_reference,omitempty"`
	Incus        *IncusBuildReference `json:"incus,omitempty"`
}

type IncusBuildReference struct {
	Project             string `json:"project"`
	WorkflowID          string `json:"workflow_id"`
	CandidateRevisionID string `json:"candidate_revision_id"`
	Attempt             int64  `json:"attempt"`
	InstanceName        string `json:"instance_name"`
	Alias               string `json:"alias"`
	Fingerprint         string `json:"fingerprint"`
}

type ArtifactReference struct {
	Runtime          string `json:"runtime"`
	OCIReference     string `json:"oci_reference,omitempty"`
	IncusAlias       string `json:"incus_alias,omitempty"`
	IncusFingerprint string `json:"incus_fingerprint,omitempty"`
}

type VerificationEnvironment struct {
	Runtime    string `json:"runtime"`
	Name       string `json:"name"`
	UID        string `json:"uid"`
	WorkflowID string `json:"workflow_id"`
	Attempt    int64  `json:"attempt"`
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

// ReproductionEvidenceResult records an initialized-state observation. It is
// intentionally distinct from a checkpoint, which describes the repaired
// state after a reference answer has run.
type ReproductionEvidenceResult struct {
	ID       string `json:"id"`
	Observed bool   `json:"observed"`
	Summary  string `json:"summary"`
	Details  string `json:"details,omitempty"`
}

type VerificationReport struct {
	Passed       bool                         `json:"passed"`
	Reproduction []ReproductionEvidenceResult `json:"reproduction,omitempty"`
	Answers      []ExecutionResult            `json:"answers"`
	Checkpoints  []CheckpointResult           `json:"checkpoints"`
	Summary      string                       `json:"summary,omitempty"`
}

// Work is the immutable execution view consumed by build, publication, and
// verification adapters. OwnerID scopes provider-side resources while
// CandidateID scopes the candidate artifact. Neither field implies an
// authoring workflow, so Catalog Release installation can use the same
// runtime mechanics without creating a GenerationWorkflow.
type Work struct {
	OwnerID       string   `json:"owner_id"`
	CandidateID   string   `json:"candidate_id"`
	ArchiveSHA256 string   `json:"archive_sha256"`
	Snapshot      Snapshot `json:"snapshot"`
	Attempt       int64    `json:"attempt"`
	// DeadlineAt is local to this one action attempt. It is not an aggregate
	// workflow deadline and retries receive a new value.
	DeadlineAt              time.Time                `json:"deadline_at"`
	Build                   *BuildOutput             `json:"build,omitempty"`
	Artifact                *ArtifactReference       `json:"artifact,omitempty"`
	VerificationEnvironment *VerificationEnvironment `json:"verification_environment,omitempty"`
}

func (w Work) Validate() error {
	if strings.TrimSpace(w.OwnerID) == "" || strings.TrimSpace(w.CandidateID) == "" ||
		!ValidSHA256(w.ArchiveSHA256) || w.Attempt <= 0 || w.DeadlineAt.IsZero() {
		return errors.New("execution work identity is incomplete")
	}
	if err := w.Snapshot.Validate(); err != nil {
		return fmt.Errorf("execution work snapshot: %w", err)
	}
	if w.Build != nil {
		if w.Build.Runtime != w.Snapshot.Runtime {
			return errors.New("execution work build output runtime is invalid")
		}
		if w.Snapshot.Runtime == scenario.RuntimeK8s {
			if w.Build.OCIReference == "" || w.Build.Incus != nil ||
				(ArtifactReference{Runtime: scenario.RuntimeK8s, OCIReference: w.Build.OCIReference}).Validate(scenario.RuntimeK8s) != nil {
				return errors.New("execution work k8s build output is invalid")
			}
		} else if w.Build.Validate(w.Snapshot.Runtime) != nil {
			return errors.New("execution work node build output is invalid")
		}
	}
	if w.Artifact != nil && w.Artifact.Validate(w.Snapshot.Runtime) != nil {
		return errors.New("execution work artifact is invalid")
	}
	if w.VerificationEnvironment != nil && w.VerificationEnvironment.Validate(w.Snapshot.Runtime) != nil {
		return errors.New("execution work verification environment is invalid")
	}
	return nil
}

// ArtifactError denotes a deterministic source or runtime-artifact defect.
// Callers retain its structured code and optional verification report rather
// than treating it as a transient provider failure.
type ArtifactError struct {
	Code    string
	Summary string
	Report  *VerificationReport
}

func (e *ArtifactError) Error() string {
	if e == nil {
		return "artifact execution failed"
	}
	return e.Summary
}

func NewArtifactError(code, summary string) error {
	return &ArtifactError{Code: strings.TrimSpace(code), Summary: strings.TrimSpace(summary)}
}

func NewArtifactErrorWithReport(code, summary string, report VerificationReport) error {
	return &ArtifactError{Code: strings.TrimSpace(code), Summary: strings.TrimSpace(summary), Report: &report}
}

func (r VerificationReport) Validate(snapshot Snapshot) error {
	if err := snapshot.Validate(); err != nil {
		return fmt.Errorf("verification report has an invalid execution snapshot: %w", err)
	}
	if strings.TrimSpace(r.Summary) == "" {
		return errors.New("verification report summary is required")
	}
	reproduced, err := r.validateReproduction(snapshot)
	if err != nil {
		return err
	}
	if !reproduced {
		if len(r.Answers) != 0 || len(r.Checkpoints) != 0 {
			return errors.New("verification report with unreproduced evidence must not contain repair results")
		}
		if r.Passed {
			return errors.New("verification report passed despite unreproduced evidence")
		}
		return nil
	}

	if !snapshot.RequiresReferenceRepair() {
		if len(r.Answers) != 0 || len(r.Checkpoints) != 0 {
			return errors.New("verification report without a reference repair must not contain repair results")
		}
		if !r.Passed {
			return errors.New("verification report failed despite reproduced phenomenon and no reference repair")
		}
		return nil
	}

	wantAnswers := make(map[string]struct{})
	switch snapshot.Runtime {
	case scenario.RuntimeNode:
		for _, node := range snapshot.Node.Nodes {
			wantAnswers[node.Name] = struct{}{}
		}
	case scenario.RuntimeK8s:
		wantAnswers["management"] = struct{}{}
	default:
		return errors.New("verification report has an unsupported runtime")
	}
	if len(r.Answers) != len(wantAnswers) {
		return errors.New("verification report does not cover every answer location")
	}
	want := make(map[string]struct{}, len(snapshot.Checkpoints))
	for _, checkpoint := range snapshot.Checkpoints {
		want[checkpoint.ID] = struct{}{}
	}
	seen := make(map[string]struct{}, len(r.Checkpoints))
	for _, checkpoint := range r.Checkpoints {
		if _, ok := want[checkpoint.ID]; !ok {
			return fmt.Errorf("verification report contains unknown checkpoint %q", checkpoint.ID)
		}
		if _, duplicate := seen[checkpoint.ID]; duplicate {
			return fmt.Errorf("verification report contains duplicate checkpoint %q", checkpoint.ID)
		}
		seen[checkpoint.ID] = struct{}{}
	}
	if len(seen) != len(want) {
		return errors.New("verification report does not cover every checkpoint")
	}
	allPassed := true
	seenAnswers := make(map[string]struct{}, len(r.Answers))
	for _, answer := range r.Answers {
		location := strings.TrimSpace(answer.Location)
		if _, ok := wantAnswers[location]; !ok {
			return fmt.Errorf("verification report contains unknown answer location %q", answer.Location)
		}
		if _, duplicate := seenAnswers[location]; duplicate {
			return fmt.Errorf("verification report contains duplicate answer location %q", location)
		}
		seenAnswers[location] = struct{}{}
		if answer.ExitCode != 0 {
			allPassed = false
		}
	}
	for _, checkpoint := range r.Checkpoints {
		if strings.TrimSpace(checkpoint.Summary) == "" || !checkpoint.Passed {
			allPassed = false
		}
	}
	if r.Passed != allPassed {
		return errors.New("verification report passed flag does not match its results")
	}
	return nil
}

func (r VerificationReport) validateReproduction(snapshot Snapshot) (bool, error) {
	if len(snapshot.Reproduction) == 0 {
		if len(r.Reproduction) != 0 {
			return false, errors.New("legacy verification report contains reproduction evidence")
		}
		return true, nil
	}
	if len(r.Reproduction) != len(snapshot.Reproduction) {
		return false, errors.New("verification report does not cover every reproduction evidence item")
	}
	expected := make(map[string]struct{}, len(snapshot.Reproduction))
	for _, evidence := range snapshot.Reproduction {
		expected[evidence.ID] = struct{}{}
	}
	observed := true
	seen := make(map[string]struct{}, len(r.Reproduction))
	for _, evidence := range r.Reproduction {
		if _, ok := expected[evidence.ID]; !ok {
			return false, fmt.Errorf("verification report contains unknown reproduction evidence %q", evidence.ID)
		}
		if _, duplicate := seen[evidence.ID]; duplicate {
			return false, fmt.Errorf("verification report contains duplicate reproduction evidence %q", evidence.ID)
		}
		if strings.TrimSpace(evidence.Summary) == "" {
			return false, fmt.Errorf("verification report has empty reproduction evidence summary for %q", evidence.ID)
		}
		seen[evidence.ID] = struct{}{}
		observed = observed && evidence.Observed
	}
	if len(seen) != len(expected) {
		return false, errors.New("verification report does not cover every reproduction evidence item")
	}
	return observed, nil
}

func (e VerificationEnvironment) Validate(runtime string) error {
	if e.Runtime != runtime || (runtime != scenario.RuntimeNode && runtime != scenario.RuntimeK8s) ||
		strings.TrimSpace(e.Name) == "" || strings.TrimSpace(e.UID) == "" || strings.TrimSpace(e.WorkflowID) == "" || e.Attempt <= 0 {
		return errors.New("verification environment identity is incomplete")
	}
	return nil
}

func (o BuildOutput) Validate(runtime string) error {
	if o.Runtime != runtime || (runtime != scenario.RuntimeNode && runtime != scenario.RuntimeK8s) {
		return errors.New("build output runtime does not match candidate")
	}
	if runtime == scenario.RuntimeK8s {
		if o.Incus != nil || (ArtifactReference{Runtime: scenario.RuntimeK8s, OCIReference: o.OCIReference}).Validate(scenario.RuntimeK8s) != nil {
			return errors.New("k8s build output is incomplete")
		}
		return nil
	}
	if o.OCIReference != "" || o.Incus == nil || o.Incus.Validate() != nil {
		return errors.New("node build output is incomplete")
	}
	return nil
}

func (r IncusBuildReference) Validate() error {
	if strings.TrimSpace(r.Project) == "" || strings.TrimSpace(r.WorkflowID) == "" || strings.TrimSpace(r.CandidateRevisionID) == "" || r.Attempt <= 0 ||
		strings.TrimSpace(r.InstanceName) == "" || strings.TrimSpace(r.Alias) == "" || !validFingerprint(r.Fingerprint) {
		return errors.New("incus build reference is incomplete")
	}
	return nil
}

func (r ArtifactReference) Validate(runtime string) error {
	if r.Runtime != runtime || (runtime != scenario.RuntimeNode && runtime != scenario.RuntimeK8s) {
		return errors.New("artifact runtime does not match candidate")
	}
	if runtime == scenario.RuntimeK8s {
		if !strings.Contains(r.OCIReference, "@sha256:") || r.IncusAlias != "" || r.IncusFingerprint != "" {
			return errors.New("k8s artifact requires an immutable OCI reference")
		}
		parts := strings.SplitN(r.OCIReference, "@", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || !ValidSHA256(parts[1]) {
			return errors.New("k8s artifact OCI reference is invalid")
		}
		return nil
	}
	if r.OCIReference != "" || strings.TrimSpace(r.IncusAlias) == "" || !validFingerprint(r.IncusFingerprint) {
		return errors.New("node artifact requires a full Incus fingerprint")
	}
	return nil
}

func ValidSHA256(value string) bool {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+64 {
		return false
	}
	for _, character := range value[len("sha256:"):] {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func (s Snapshot) Validate() error {
	if s.Runtime != scenario.RuntimeNode && s.Runtime != scenario.RuntimeK8s {
		return errors.New("candidate snapshot has an invalid runtime")
	}
	if s.RequiresReferenceRepair() && len(s.Checkpoints) == 0 {
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
	reproduction := make(map[string]struct{}, len(s.Reproduction))
	for _, evidence := range s.Reproduction {
		if strings.TrimSpace(evidence.ID) == "" {
			return errors.New("candidate snapshot has an empty reproduction evidence id")
		}
		if _, duplicate := reproduction[evidence.ID]; duplicate {
			return errors.New("candidate snapshot has duplicate reproduction evidence ids")
		}
		reproduction[evidence.ID] = struct{}{}
	}
	if s.Runtime == scenario.RuntimeNode {
		if s.Node == nil || s.K8s != nil || len(s.Node.Nodes) == 0 || !validFingerprint(s.Node.BaseImageFingerprint) {
			return errors.New("node candidate snapshot is incomplete")
		}
		if strings.TrimSpace(s.Node.ProfileRevision) == "" || strings.TrimSpace(s.Node.NetworkPolicyRevision) == "" || strings.TrimSpace(s.Node.Resources.CPU) == "" || strings.TrimSpace(s.Node.Resources.Memory) == "" || strings.TrimSpace(s.Node.Resources.RootDisk) == "" || s.Node.Resources.Processes <= 0 {
			return errors.New("node candidate resource snapshot is incomplete")
		}
		nodes := make(map[string]struct{}, len(s.Node.Nodes))
		for _, node := range s.Node.Nodes {
			if strings.TrimSpace(node.Name) == "" || strings.TrimSpace(node.Title) == "" {
				return errors.New("node candidate snapshot has an incomplete node")
			}
			if _, duplicate := nodes[node.Name]; duplicate {
				return errors.New("node candidate snapshot has duplicate nodes")
			}
			nodes[node.Name] = struct{}{}
		}
		for _, checkpoint := range s.Checkpoints {
			if _, ok := nodes[checkpoint.Node]; !ok {
				return errors.New("node candidate checkpoint references an unknown node")
			}
		}
		for _, evidence := range s.Reproduction {
			if _, ok := nodes[evidence.Node]; !ok {
				return errors.New("node candidate reproduction evidence references an unknown node")
			}
		}
	} else if s.K8s == nil || s.Node != nil || !strings.Contains(s.K8s.BaseImageDigest, "@sha256:") {
		return errors.New("k8s candidate snapshot is incomplete")
	} else {
		if strings.TrimSpace(s.K8s.ProfileRevision) == "" || strings.TrimSpace(s.K8s.Version) == "" || !strings.Contains(s.K8s.ManagementTerminalImage, "@sha256:") {
			return errors.New("k8s candidate resource snapshot is incomplete")
		}
		if err := s.K8s.Network.Validate(); err != nil {
			return fmt.Errorf("k8s candidate network snapshot: %w", err)
		}
		if err := (environment.VK8sResources{
			ControlPlaneCPU: s.K8s.Resources.ControlPlaneCPU, ControlPlaneMemory: s.K8s.Resources.ControlPlaneMemory,
			ControlPlaneEphemeralStorage: s.K8s.Resources.ControlPlaneEphemeralStorage,
			WorkloadCPU:                  s.K8s.Resources.WorkloadCPU, WorkloadMemory: s.K8s.Resources.WorkloadMemory,
			WorkloadEphemeralStorage: s.K8s.Resources.WorkloadEphemeralStorage,
			QuotaCPU:                 s.K8s.Resources.QuotaCPU, QuotaMemory: s.K8s.Resources.QuotaMemory,
			QuotaEphemeralStorage: s.K8s.Resources.QuotaEphemeralStorage,
		}).Validate(); err != nil {
			return fmt.Errorf("k8s candidate resource snapshot: %w", err)
		}
		for _, checkpoint := range s.Checkpoints {
			if strings.TrimSpace(checkpoint.Node) != "" {
				return errors.New("k8s candidate checkpoint declares a node")
			}
		}
		for _, evidence := range s.Reproduction {
			if strings.TrimSpace(evidence.Node) != "" {
				return errors.New("k8s candidate reproduction evidence declares a node")
			}
		}
	}
	return nil
}

func (s Snapshot) RequiresReferenceRepair() bool {
	// Old persisted snapshots necessarily had answer and checkpoint assets.
	return s.ReferenceRepair == nil || *s.ReferenceRepair
}

func validFingerprint(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
