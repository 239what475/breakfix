package generation

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/content/challenge"
	"github.com/breakfix/breakfix/internal/domain/environment"
)

var (
	ErrCandidateNotFound     = errors.New("candidate revision not found")
	ErrCandidateInvalidState = errors.New("candidate revision is not in the required state")
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
	Resources             NodeResources  `json:"resources"`
}

type NodeResources struct {
	CPU       string `json:"cpu"`
	Memory    string `json:"memory"`
	Processes int64  `json:"processes"`
	RootDisk  string `json:"root_disk"`
}

type K8sRuntimeSnapshot struct {
	BaseImageDigest         string       `json:"base_image_digest"`
	ProfileRevision         string       `json:"profile_revision"`
	Version                 string       `json:"version"`
	ManagementTerminalImage string       `json:"management_terminal_image"`
	Resources               K8sResources `json:"resources"`
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

type ExecutionSnapshot struct {
	Runtime     string               `json:"runtime"`
	Checkpoints []CheckpointSnapshot `json:"checkpoints"`
	Node        *NodeRuntimeSnapshot `json:"node,omitempty"`
	K8s         *K8sRuntimeSnapshot  `json:"k8s,omitempty"`
}

type BuildOutput struct {
	Runtime          string               `json:"runtime"`
	OCIArchivePath   string               `json:"oci_archive_path,omitempty"`
	OCIArchiveSHA256 string               `json:"oci_archive_sha256,omitempty"`
	Incus            *IncusBuildReference `json:"incus,omitempty"`
}

// IncusBuildReference is the complete identity of one fenced, stopped-image
// build attempt. Publisher and cleanup never reconstruct this identity from a
// candidate ID or scan an Incus Project.
type IncusBuildReference struct {
	Project      string `json:"project"`
	WorkflowID   string `json:"workflow_id"`
	Attempt      int64  `json:"attempt"`
	InstanceName string `json:"instance_name"`
	Alias        string `json:"alias"`
	Fingerprint  string `json:"fingerprint"`
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

type VerificationReport struct {
	Passed      bool               `json:"passed"`
	Answers     []ExecutionResult  `json:"answers"`
	Checkpoints []CheckpointResult `json:"checkpoints"`
	Summary     string             `json:"summary,omitempty"`
}

func (r VerificationReport) Validate(snapshot ExecutionSnapshot) error {
	if err := snapshot.Validate(); err != nil {
		return fmt.Errorf("verification report has an invalid execution snapshot: %w", err)
	}
	if strings.TrimSpace(r.Summary) == "" {
		return errors.New("verification report summary is required")
	}
	wantAnswers := make(map[string]struct{})
	switch snapshot.Runtime {
	case challenge.RuntimeNode:
		for _, node := range snapshot.Node.Nodes {
			wantAnswers[node.Name] = struct{}{}
		}
	case challenge.RuntimeK8s:
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

type Publication struct {
	ChallengeID string             `json:"challenge_id"`
	SourceSlug  string             `json:"source_slug"`
	TargetPath  string             `json:"target_path"`
	RequestedAt time.Time          `json:"requested_at"`
	Artifact    *ArtifactReference `json:"artifact,omitempty"`
}

func (p Publication) ValidateIntent() error {
	if !challenge.ValidID(p.ChallengeID) || !challenge.ValidSourceSlug(p.SourceSlug) || p.TargetPath != p.SourceSlug || p.RequestedAt.IsZero() || p.Artifact != nil {
		return errors.New("challenge publication intent is incomplete")
	}
	return nil
}

func (e VerificationEnvironment) Validate(runtime string) error {
	if e.Runtime != runtime || (runtime != challenge.RuntimeNode && runtime != challenge.RuntimeK8s) ||
		strings.TrimSpace(e.Name) == "" || strings.TrimSpace(e.UID) == "" || strings.TrimSpace(e.WorkflowID) == "" || e.Attempt <= 0 {
		return errors.New("verification environment identity is incomplete")
	}
	return nil
}

type Revision struct {
	ID                 string                   `json:"id"`
	Source             Source                   `json:"source"`
	SourceRevision     string                   `json:"source_revision"`
	GeneratorSessionID string                   `json:"generator_session_id,omitempty"`
	GeneratorRunID     string                   `json:"generator_run_id,omitempty"`
	JudgeRunID         string                   `json:"judge_run_id,omitempty"`
	ArchivePath        string                   `json:"-"`
	ArchiveSHA256      string                   `json:"archive_sha256"`
	Snapshot           ExecutionSnapshot        `json:"snapshot"`
	Build              *BuildOutput             `json:"build,omitempty"`
	Artifact           *ArtifactReference       `json:"artifact,omitempty"`
	VerifyEnvironment  *VerificationEnvironment `json:"verify_environment,omitempty"`
	Verification       *VerificationReport      `json:"verification,omitempty"`
	Failure            *Failure                 `json:"failure,omitempty"`
	Publication        *Publication             `json:"publication,omitempty"`
	CreatedAt          time.Time                `json:"created_at"`
	UpdatedAt          time.Time                `json:"updated_at"`
	VerifiedAt         *time.Time               `json:"verified_at,omitempty"`
	PublishedAt        *time.Time               `json:"published_at,omitempty"`
}

// WorkerView deliberately excludes ArchivePath. Workers access candidate
// bytes only through attempt-fenced Server endpoints and never learn the
// Server volume layout.
type WorkerView struct {
	ID                string                   `json:"id"`
	SourceRevision    string                   `json:"source_revision"`
	ArchiveSHA256     string                   `json:"archive_sha256"`
	Snapshot          ExecutionSnapshot        `json:"snapshot"`
	Build             *BuildOutput             `json:"build,omitempty"`
	Artifact          *ArtifactReference       `json:"artifact,omitempty"`
	VerifyEnvironment *VerificationEnvironment `json:"verify_environment,omitempty"`
	Verification      *VerificationReport      `json:"verification,omitempty"`
	Failure           *Failure                 `json:"failure,omitempty"`
	Publication       *Publication             `json:"publication,omitempty"`
}

func (r Revision) WorkerView() WorkerView {
	var build *BuildOutput
	if r.Build != nil {
		copy := *r.Build
		copy.OCIArchivePath = ""
		build = &copy
	}
	var failure *Failure
	if r.Failure != nil {
		copy := *r.Failure
		failure = &copy
	}
	return WorkerView{
		ID: r.ID, SourceRevision: r.SourceRevision, ArchiveSHA256: r.ArchiveSHA256, Snapshot: r.Snapshot,
		Build: build, Artifact: r.Artifact, VerifyEnvironment: r.VerifyEnvironment,
		Verification: r.Verification, Failure: failure, Publication: r.Publication,
	}
}

// IDForGeneratorRun is stable across a lost HTTP response while remaining
// opaque to users and independent of challenge content.
func IDForGeneratorRun(runID string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(runID)))
	return "candidate-" + fmt.Sprintf("%x", sum[:12])
}

func (o BuildOutput) Validate(runtime string) error {
	if o.Runtime != runtime || (runtime != challenge.RuntimeNode && runtime != challenge.RuntimeK8s) {
		return errors.New("build output runtime does not match candidate")
	}
	if runtime == challenge.RuntimeK8s {
		if strings.TrimSpace(o.OCIArchivePath) == "" || !ValidSHA256(o.OCIArchiveSHA256) || o.Incus != nil {
			return errors.New("k8s build output is incomplete")
		}
		return nil
	}
	if o.OCIArchivePath != "" || o.OCIArchiveSHA256 != "" || o.Incus == nil || o.Incus.Validate() != nil {
		return errors.New("node build output is incomplete")
	}
	return nil
}

func (r IncusBuildReference) Validate() error {
	if strings.TrimSpace(r.Project) == "" || strings.TrimSpace(r.WorkflowID) == "" || r.Attempt <= 0 ||
		strings.TrimSpace(r.InstanceName) == "" || strings.TrimSpace(r.Alias) == "" || !validFingerprint(r.Fingerprint) {
		return errors.New("incus build reference is incomplete")
	}
	return nil
}

func (r ArtifactReference) Validate(runtime string) error {
	if r.Runtime != runtime || (runtime != challenge.RuntimeNode && runtime != challenge.RuntimeK8s) {
		return errors.New("artifact runtime does not match candidate")
	}
	if runtime == challenge.RuntimeK8s {
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

func (r Revision) ValidateForCreate() error {
	if strings.TrimSpace(r.ID) == "" || !r.Source.Valid() || strings.TrimSpace(r.SourceRevision) == "" {
		return errors.New("candidate revision requires identity and source revision")
	}
	if r.Source.Kind == SourceAuthoring && (strings.TrimSpace(r.GeneratorSessionID) == "" || strings.TrimSpace(r.GeneratorRunID) == "") {
		return errors.New("authoring candidate revision requires generator lineage")
	}
	if strings.TrimSpace(r.ArchivePath) == "" || !ValidSHA256(r.ArchiveSHA256) {
		return errors.New("candidate revision requires an immutable archive")
	}
	if r.Build != nil || r.Artifact != nil || r.VerifyEnvironment != nil || r.Verification != nil || r.Failure != nil || r.Publication != nil {
		return errors.New("new candidate revision must not contain stage outputs")
	}
	return r.Snapshot.Validate()
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
	} else if s.K8s == nil || s.Node != nil || !strings.Contains(s.K8s.BaseImageDigest, "@sha256:") {
		return errors.New("k8s candidate snapshot is incomplete")
	} else {
		if strings.TrimSpace(s.K8s.ProfileRevision) == "" || strings.TrimSpace(s.K8s.Version) == "" || !strings.Contains(s.K8s.ManagementTerminalImage, "@sha256:") {
			return errors.New("k8s candidate resource snapshot is incomplete")
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
	}
	return nil
}
