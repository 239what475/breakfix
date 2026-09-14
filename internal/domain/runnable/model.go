// Package runnable defines the product-neutral, immutable contract consumed by
// runtime builders, providers, and verifiers. Content modules compile their
// own revisions into this package; this package deliberately has no knowledge
// of content, publication, or learning terminology.
package runnable

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	// FormatVersion is the only wire-format accepted by this package. A later
	// format must use a new value rather than silently reinterpret fields.
	FormatVersion = "v1"

	MaxIDLength                   = 64
	MaxReferenceLength            = 4096
	MaxEntrypointLength           = 512
	MaxSummaryLength              = 1024
	MaxDetailsLength              = 16 * 1024
	MaxOutputReferenceCount       = 32
	MaxValidationPhases           = 32
	MaxActionsPerPhase            = 64
	MaxAssertionsPerPhase         = 64
	MaxActionTimeoutSeconds       = 60 * 60
	MaxPhaseTimeoutSeconds        = 4 * 60 * 60
	MaxLifecycleTimeout           = 4 * 60 * 60
	MaxLifetimeSeconds      int64 = 7 * 24 * 60 * 60
)

type Runtime string

const (
	RuntimeNode Runtime = "node"
	RuntimeK8s  Runtime = "k8s"
)

func (r Runtime) Valid() bool {
	return r == RuntimeNode || r == RuntimeK8s
}

type Permission string

const (
	PermissionReadOnly  Permission = "read-only"
	PermissionReadWrite Permission = "read-write"
)

func (p Permission) Valid() bool {
	return p == PermissionReadOnly || p == PermissionReadWrite
}

type NetworkScope string

const (
	NetworkNone     NetworkScope = "none"
	NetworkIsolated NetworkScope = "isolated"
	NetworkPrivate  NetworkScope = "private"
)

func (n NetworkScope) Valid() bool {
	return n == NetworkNone || n == NetworkIsolated || n == NetworkPrivate
}

type PhaseExecution string

const (
	PhaseSequential PhaseExecution = "sequential"
	PhaseParallel   PhaseExecution = "parallel"
)

func (p PhaseExecution) Valid() bool {
	return p == PhaseSequential || p == PhaseParallel
}

type FailureClass string

const (
	FailureArtifact       FailureClass = "artifact"
	FailureInfrastructure FailureClass = "infrastructure"
)

func (f FailureClass) Valid() bool {
	return f == FailureArtifact || f == FailureInfrastructure
}

// ContentIdentity preserves provenance and auditing information without
// introducing any product-specific runtime behavior.
type ContentIdentity struct {
	Kind     string `json:"content_kind"`
	ID       string `json:"content_id"`
	Revision string `json:"content_revision"`
}

// ResourceLimits are the resolved, provider-neutral limits for one runtime
// profile. Provider adapters may apply stricter limits, but cannot exceed
// these values.
type ResourceLimits struct {
	CPU                string `json:"cpu"`
	MemoryBytes        int64  `json:"memory_bytes"`
	EphemeralBytes     int64  `json:"ephemeral_bytes"`
	MaxProcesses       int64  `json:"max_processes"`
	MaxConcurrentTasks int64  `json:"max_concurrent_tasks"`
}

// TargetLocation names one fixed execution location exposed by a runtime
// profile. Its kind is intentionally generic (for example, "node" or
// "management") and must not alter worker behavior.
type TargetLocation struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// ExecutionBoundary is a platform-approved capability. Actions and
// assertions select one by ID; they cannot introduce a target, permission, or
// network scope that the runtime profile did not freeze.
type ExecutionBoundary struct {
	ID         string         `json:"id"`
	Target     TargetLocation `json:"target"`
	Permission Permission     `json:"permission"`
	Network    NetworkScope   `json:"network"`
	MaxTimeout int64          `json:"max_timeout_seconds"`
}

// RuntimeProfile freezes the provider-independent runtime inputs. Concrete
// provider SDK values belong in adapters and must never leak into this type.
type RuntimeProfile struct {
	Runtime             Runtime             `json:"runtime"`
	ProfileRevision     string              `json:"profile_revision"`
	BaseImage           string              `json:"base_image"`
	SoftwareVersions    map[string]string   `json:"software_versions"`
	Resources           ResourceLimits      `json:"resources"`
	Network             NetworkScope        `json:"network"`
	Topology            string              `json:"topology"`
	ExecutionBoundaries []ExecutionBoundary `json:"execution_boundaries"`
}

// SourceArchive identifies exact source bytes. Reference must point to an
// immutable object; the digest remains the authority for byte identity.
type SourceArchive struct {
	FormatVersion string `json:"format_version"`
	Reference     string `json:"reference"`
	Digest        string `json:"digest"`
}

// ActionSpec declares a deterministic state-changing execution. The entrypoint
// is a safe relative path inside SourceArchive, never a shell fragment.
type ActionSpec struct {
	ID                string         `json:"id"`
	Entrypoint        string         `json:"entrypoint"`
	Target            TargetLocation `json:"target"`
	BoundaryID        string         `json:"boundary_id"`
	TimeoutSeconds    int64          `json:"timeout_seconds"`
	ExpectedExitCodes []int          `json:"expected_exit_codes"`
}

// AssertionSpec declares a read-only observation. Assertion output must use
// the JSON protocol parsed by the runtime verifier.
type AssertionSpec struct {
	ID             string         `json:"id"`
	Entrypoint     string         `json:"entrypoint"`
	Target         TargetLocation `json:"target"`
	BoundaryID     string         `json:"boundary_id"`
	TimeoutSeconds int64          `json:"timeout_seconds"`
}

type ValidationPhase struct {
	ID             string          `json:"id"`
	TimeoutSeconds int64           `json:"timeout_seconds"`
	Execution      PhaseExecution  `json:"execution"`
	Actions        []ActionSpec    `json:"actions"`
	Assertions     []AssertionSpec `json:"assertions"`
}

type ValidationPlan struct {
	FormatVersion string            `json:"format_version"`
	Phases        []ValidationPhase `json:"phases"`
}

// LifecyclePolicy is interpreted only once when an environment is created.
// Environments retain resolved timestamps instead of reinterpreting content
// policy during reset, stop, or reap.
type LifecyclePolicy struct {
	CreateTimeoutSeconds int64 `json:"create_timeout_seconds"`
	ResetTimeoutSeconds  int64 `json:"reset_timeout_seconds"`
	StopTimeoutSeconds   int64 `json:"stop_timeout_seconds"`
	ReapTimeoutSeconds   int64 `json:"reap_timeout_seconds"`
	IdleTTLSeconds       int64 `json:"idle_ttl_seconds"`
	MaxLifetimeSeconds   int64 `json:"max_lifetime_seconds"`
}

// RunnableSpec is the immutable pre-build contract. Its digest is computed
// from its canonical representation and intentionally is not a mutable JSON
// field on the spec itself.
type RunnableSpec struct {
	FormatVersion   string          `json:"format_version"`
	Identity        ContentIdentity `json:"identity"`
	RuntimeProfile  RuntimeProfile  `json:"runtime_profile"`
	Source          SourceArchive   `json:"source"`
	Initialization  []ActionSpec    `json:"initialization"`
	ValidationPlan  ValidationPlan  `json:"validation_plan"`
	LifecyclePolicy LifecyclePolicy `json:"lifecycle_policy"`
}

// ArtifactReference is a build result, not a business aggregate. Its digest
// and BuiltFromSpecDigest form an immutable link to the exact pre-build spec.
type ArtifactReference struct {
	FormatVersion       string  `json:"format_version"`
	Runtime             Runtime `json:"runtime"`
	ProviderReference   string  `json:"provider_reference"`
	ArtifactDigest      string  `json:"artifact_digest"`
	BuiltFromSpecDigest string  `json:"built_from_spec_digest"`
	BuilderVersion      string  `json:"builder_version"`
}

// RunnableRevision is the immutable binding of a RunnableSpec and its
// ArtifactReference. Environments and publication boundaries consume this
// complete value, not a spec and artifact supplied independently.
type RunnableRevision struct {
	FormatVersion string            `json:"format_version"`
	Spec          RunnableSpec      `json:"spec"`
	Artifact      ArtifactReference `json:"artifact"`
}

// ImmutableReference identifies an immutable raw output or log object.
type ImmutableReference struct {
	Reference string `json:"reference"`
	Digest    string `json:"digest"`
	SizeBytes int64  `json:"size_bytes"`
}

type ActionResult struct {
	ID       string               `json:"id"`
	ExitCode int                  `json:"exit_code"`
	Summary  string               `json:"summary"`
	Outputs  []ImmutableReference `json:"outputs"`
}

type AssertionResult struct {
	ID        string               `json:"id"`
	Satisfied bool                 `json:"satisfied"`
	Summary   string               `json:"summary"`
	Details   string               `json:"details,omitempty"`
	Outputs   []ImmutableReference `json:"outputs"`
}

type PhaseResult struct {
	ID         string            `json:"id"`
	Actions    []ActionResult    `json:"actions"`
	Assertions []AssertionResult `json:"assertions"`
}

type EnvironmentIdentity struct {
	ID            string `json:"id"`
	Provider      string `json:"provider"`
	ProfileDigest string `json:"profile_digest"`
}

type VerificationFailure struct {
	Class     FailureClass `json:"class"`
	Component string       `json:"component"`
	Reason    string       `json:"reason"`
	Message   string       `json:"message"`
}

// VerificationReport is an immutable record of one attempt. Phase results
// are the only source of action and assertion outcomes; this type deliberately
// has no duplicate top-level result projection.
type VerificationReport struct {
	FormatVersion          string               `json:"format_version"`
	RunnableRevisionDigest string               `json:"runnable_revision_digest"`
	Environment            EnvironmentIdentity  `json:"environment"`
	Attempt                int64                `json:"attempt"`
	Phases                 []PhaseResult        `json:"phases"`
	Passed                 bool                 `json:"passed"`
	Failure                *VerificationFailure `json:"failure,omitempty"`
	CreatedAt              time.Time            `json:"created_at"`
}

// ArtifactFailure marks deterministic archive, contract, or protocol
// defects. Providers can classify these separately from retryable failures.
type ArtifactFailure struct {
	Code    string
	Summary string
}

func (e *ArtifactFailure) Error() string {
	if e == nil || strings.TrimSpace(e.Summary) == "" {
		return "runnable artifact failure"
	}
	return e.Summary
}

func NewArtifactFailure(code, summary string) error {
	return &ArtifactFailure{Code: strings.TrimSpace(code), Summary: strings.TrimSpace(summary)}
}

func invalid(field, message string) error {
	return fmt.Errorf("runnable %s %s", field, message)
}

func require(condition bool, field, message string) error {
	if !condition {
		return invalid(field, message)
	}
	return nil
}

func requiredString(value, field string, maximum int) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return invalid(field, "is required")
	}
	if len(value) > maximum {
		return invalid(field, "is too long")
	}
	return nil
}

func unsupported(value, field string) error {
	return errors.New("runnable " + field + " is unsupported: " + value)
}
