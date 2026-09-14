package runnable

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
)

var stableID = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

func ValidDigest(value string) bool {
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

func validateID(value, field string) error {
	value = strings.TrimSpace(value)
	if len(value) == 0 || len(value) > MaxIDLength || !stableID.MatchString(value) {
		return invalid(field, "must be a stable lowercase identifier")
	}
	return nil
}

func (v ContentIdentity) Validate() error {
	if err := validateID(v.Kind, "identity.content_kind"); err != nil {
		return err
	}
	if err := validateID(v.ID, "identity.content_id"); err != nil {
		return err
	}
	return requiredString(v.Revision, "identity.content_revision", MaxReferenceLength)
}

func (r ResourceLimits) Validate() error {
	if err := requiredString(r.CPU, "runtime_profile.resources.cpu", 128); err != nil {
		return err
	}
	if r.MemoryBytes <= 0 || r.EphemeralBytes <= 0 || r.MaxProcesses <= 0 || r.MaxConcurrentTasks <= 0 {
		return invalid("runtime_profile.resources", "must contain positive limits")
	}
	return nil
}

func (t TargetLocation) Validate(field string) error {
	if err := validateID(t.Kind, field+".kind"); err != nil {
		return err
	}
	return validateID(t.ID, field+".id")
}

func (b ExecutionBoundary) Validate() error {
	if err := validateID(b.ID, "runtime_profile.execution_boundaries.id"); err != nil {
		return err
	}
	if err := b.Target.Validate("runtime_profile.execution_boundaries.target"); err != nil {
		return err
	}
	if !b.Permission.Valid() {
		return unsupported(string(b.Permission), "runtime_profile.execution_boundaries.permission")
	}
	if !b.Network.Valid() {
		return unsupported(string(b.Network), "runtime_profile.execution_boundaries.network")
	}
	if b.MaxTimeout <= 0 || b.MaxTimeout > MaxActionTimeoutSeconds {
		return invalid("runtime_profile.execution_boundaries.max_timeout_seconds", "is outside the platform limit")
	}
	return nil
}

func (p RuntimeProfile) Validate() error {
	if !p.Runtime.Valid() {
		return unsupported(string(p.Runtime), "runtime_profile.runtime")
	}
	if err := requiredString(p.ProfileRevision, "runtime_profile.profile_revision", MaxReferenceLength); err != nil {
		return err
	}
	if err := requiredString(p.BaseImage, "runtime_profile.base_image", MaxReferenceLength); err != nil {
		return err
	}
	if !p.Network.Valid() {
		return unsupported(string(p.Network), "runtime_profile.network")
	}
	if err := requiredString(p.Topology, "runtime_profile.topology", MaxSummaryLength); err != nil {
		return err
	}
	if err := p.Resources.Validate(); err != nil {
		return err
	}
	if len(p.SoftwareVersions) == 0 || len(p.SoftwareVersions) > 64 {
		return invalid("runtime_profile.software_versions", "must contain between one and 64 entries")
	}
	for name, version := range p.SoftwareVersions {
		if err := validateID(name, "runtime_profile.software_versions key"); err != nil {
			return err
		}
		if err := requiredString(version, "runtime_profile.software_versions value", MaxIDLength); err != nil {
			return err
		}
	}
	if len(p.ExecutionBoundaries) == 0 || len(p.ExecutionBoundaries) > MaxActionsPerPhase {
		return invalid("runtime_profile.execution_boundaries", "must contain approved targets")
	}
	seen := make(map[string]struct{}, len(p.ExecutionBoundaries))
	for _, boundary := range p.ExecutionBoundaries {
		if err := boundary.Validate(); err != nil {
			return err
		}
		if _, exists := seen[boundary.ID]; exists {
			return invalid("runtime_profile.execution_boundaries", "contains duplicate identifiers")
		}
		seen[boundary.ID] = struct{}{}
	}
	return nil
}

func (s SourceArchive) Validate() error {
	if s.FormatVersion != FormatVersion {
		return unsupported(s.FormatVersion, "source.format_version")
	}
	if err := requiredString(s.Reference, "source.reference", MaxReferenceLength); err != nil {
		return err
	}
	if !ValidDigest(s.Digest) {
		return invalid("source.digest", "must be a sha256 digest")
	}
	return nil
}

func validateEntrypoint(value, field string) error {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > MaxEntrypointLength || strings.HasPrefix(value, "/") || path.Clean(value) != value || value == "." || strings.HasPrefix(value, "../") {
		return invalid(field, "must be a safe relative archive path")
	}
	return nil
}

func (a ActionSpec) Validate(profile RuntimeProfile, field string) error {
	if err := validateID(a.ID, field+".id"); err != nil {
		return err
	}
	if err := validateEntrypoint(a.Entrypoint, field+".entrypoint"); err != nil {
		return err
	}
	if err := a.Target.Validate(field + ".target"); err != nil {
		return err
	}
	boundary, ok := profile.boundary(a.BoundaryID)
	if !ok || boundary.Target != a.Target || boundary.Permission != PermissionReadWrite {
		return invalid(field+".boundary_id", "must select a read-write declared target")
	}
	if a.TimeoutSeconds <= 0 || a.TimeoutSeconds > boundary.MaxTimeout || a.TimeoutSeconds > MaxActionTimeoutSeconds {
		return invalid(field+".timeout_seconds", "is outside its execution boundary")
	}
	if len(a.ExpectedExitCodes) == 0 || len(a.ExpectedExitCodes) > 16 || !slices.IsSorted(a.ExpectedExitCodes) {
		return invalid(field+".expected_exit_codes", "must be a non-empty sorted set")
	}
	for index, value := range a.ExpectedExitCodes {
		if value < 0 || value > 255 || (index > 0 && value == a.ExpectedExitCodes[index-1]) {
			return invalid(field+".expected_exit_codes", "must contain unique process exit codes")
		}
	}
	return nil
}

func (a AssertionSpec) Validate(profile RuntimeProfile, field string) error {
	if err := validateID(a.ID, field+".id"); err != nil {
		return err
	}
	if err := validateEntrypoint(a.Entrypoint, field+".entrypoint"); err != nil {
		return err
	}
	if err := a.Target.Validate(field + ".target"); err != nil {
		return err
	}
	boundary, ok := profile.boundary(a.BoundaryID)
	if !ok || boundary.Target != a.Target || boundary.Permission != PermissionReadOnly {
		return invalid(field+".boundary_id", "must select a read-only declared target")
	}
	if a.TimeoutSeconds <= 0 || a.TimeoutSeconds > boundary.MaxTimeout || a.TimeoutSeconds > MaxActionTimeoutSeconds {
		return invalid(field+".timeout_seconds", "is outside its execution boundary")
	}
	return nil
}

func (p RuntimeProfile) boundary(id string) (ExecutionBoundary, bool) {
	for _, boundary := range p.ExecutionBoundaries {
		if boundary.ID == id {
			return boundary, true
		}
	}
	return ExecutionBoundary{}, false
}

func (p ValidationPlan) Validate(profile RuntimeProfile) error {
	if p.FormatVersion != FormatVersion {
		return unsupported(p.FormatVersion, "validation_plan.format_version")
	}
	if len(p.Phases) == 0 || len(p.Phases) > MaxValidationPhases {
		return invalid("validation_plan.phases", "must contain between one and 32 phases")
	}
	phaseIDs := make(map[string]struct{}, len(p.Phases))
	actionIDs := make(map[string]struct{})
	assertionIDs := make(map[string]struct{})
	for phaseIndex, phase := range p.Phases {
		field := fmt.Sprintf("validation_plan.phases[%d]", phaseIndex)
		if err := validateID(phase.ID, field+".id"); err != nil {
			return err
		}
		if _, exists := phaseIDs[phase.ID]; exists {
			return invalid(field+".id", "is duplicated")
		}
		phaseIDs[phase.ID] = struct{}{}
		if !phase.Execution.Valid() {
			return unsupported(string(phase.Execution), field+".execution")
		}
		if phase.TimeoutSeconds <= 0 || phase.TimeoutSeconds > MaxPhaseTimeoutSeconds {
			return invalid(field+".timeout_seconds", "is outside the platform limit")
		}
		if len(phase.Actions) > MaxActionsPerPhase || len(phase.Assertions) > MaxAssertionsPerPhase || (len(phase.Actions) == 0 && len(phase.Assertions) == 0) {
			return invalid(field, "must have bounded actions or assertions")
		}
		for actionIndex, action := range phase.Actions {
			if err := action.Validate(profile, fmt.Sprintf("%s.actions[%d]", field, actionIndex)); err != nil {
				return err
			}
			if _, exists := actionIDs[action.ID]; exists {
				return invalid(field+".actions", "contains a duplicate action id")
			}
			actionIDs[action.ID] = struct{}{}
		}
		for assertionIndex, assertion := range phase.Assertions {
			if err := assertion.Validate(profile, fmt.Sprintf("%s.assertions[%d]", field, assertionIndex)); err != nil {
				return err
			}
			if _, exists := assertionIDs[assertion.ID]; exists {
				return invalid(field+".assertions", "contains a duplicate assertion id")
			}
			assertionIDs[assertion.ID] = struct{}{}
		}
	}
	return nil
}

func (p LifecyclePolicy) Validate() error {
	for field, value := range map[string]int64{
		"create_timeout_seconds": p.CreateTimeoutSeconds,
		"reset_timeout_seconds":  p.ResetTimeoutSeconds,
		"stop_timeout_seconds":   p.StopTimeoutSeconds,
		"reap_timeout_seconds":   p.ReapTimeoutSeconds,
	} {
		if value <= 0 || value > MaxLifecycleTimeout {
			return invalid("lifecycle_policy."+field, "is outside the platform limit")
		}
	}
	if p.IdleTTLSeconds <= 0 || p.IdleTTLSeconds > MaxLifetimeSeconds || p.MaxLifetimeSeconds <= 0 || p.MaxLifetimeSeconds > MaxLifetimeSeconds || p.IdleTTLSeconds > p.MaxLifetimeSeconds {
		return invalid("lifecycle_policy", "has invalid idle or maximum lifetime")
	}
	return nil
}

func (s RunnableSpec) Validate() error {
	if s.FormatVersion != FormatVersion {
		return unsupported(s.FormatVersion, "spec.format_version")
	}
	if err := s.Identity.Validate(); err != nil {
		return err
	}
	if err := s.RuntimeProfile.Validate(); err != nil {
		return err
	}
	if err := s.Source.Validate(); err != nil {
		return err
	}
	if len(s.Initialization) == 0 || len(s.Initialization) > MaxActionsPerPhase {
		return invalid("initialization", "must contain bounded initialization actions")
	}
	initializationIDs := make(map[string]struct{}, len(s.Initialization))
	for index, action := range s.Initialization {
		if err := action.Validate(s.RuntimeProfile, fmt.Sprintf("initialization[%d]", index)); err != nil {
			return err
		}
		if _, exists := initializationIDs[action.ID]; exists {
			return invalid("initialization", "contains a duplicate action id")
		}
		initializationIDs[action.ID] = struct{}{}
	}
	if err := s.ValidationPlan.Validate(s.RuntimeProfile); err != nil {
		return err
	}
	for _, phase := range s.ValidationPlan.Phases {
		if phase.ID == "initialization" {
			return invalid("validation_plan.phases", "must not use the reserved initialization phase id")
		}
		for _, action := range phase.Actions {
			if _, exists := initializationIDs[action.ID]; exists {
				return invalid("validation_plan", "reuses an initialization action id")
			}
		}
	}
	if err := s.LifecyclePolicy.Validate(); err != nil {
		return err
	}
	return nil
}

func (a ArtifactReference) Validate() error {
	if a.FormatVersion != FormatVersion {
		return unsupported(a.FormatVersion, "artifact.format_version")
	}
	if !a.Runtime.Valid() {
		return unsupported(string(a.Runtime), "artifact.runtime")
	}
	if err := requiredString(a.ProviderReference, "artifact.provider_reference", MaxReferenceLength); err != nil {
		return err
	}
	if !ValidDigest(a.ArtifactDigest) || !ValidDigest(a.BuiltFromSpecDigest) {
		return invalid("artifact", "must bind valid artifact and spec digests")
	}
	if !strings.Contains(a.ProviderReference, "@"+a.ArtifactDigest) {
		return invalid("artifact.provider_reference", "must be pinned to artifact_digest")
	}
	return requiredString(a.BuilderVersion, "artifact.builder_version", MaxIDLength)
}

func (r RunnableRevision) Validate() error {
	if r.FormatVersion != FormatVersion {
		return unsupported(r.FormatVersion, "revision.format_version")
	}
	if err := r.Spec.Validate(); err != nil {
		return fmt.Errorf("runnable revision spec: %w", err)
	}
	if err := r.Artifact.Validate(); err != nil {
		return fmt.Errorf("runnable revision artifact: %w", err)
	}
	if r.Artifact.Runtime != r.Spec.RuntimeProfile.Runtime {
		return errors.New("runnable revision artifact runtime does not match spec")
	}
	digest, err := r.Spec.Digest()
	if err != nil {
		return fmt.Errorf("runnable revision spec digest: %w", err)
	}
	if r.Artifact.BuiltFromSpecDigest != digest {
		return errors.New("runnable revision artifact was built from another spec")
	}
	return nil
}

func (r ImmutableReference) Validate(field string) error {
	if err := requiredString(r.Reference, field+".reference", MaxReferenceLength); err != nil {
		return err
	}
	if !ValidDigest(r.Digest) || r.SizeBytes < 0 {
		return invalid(field, "must identify immutable bounded output")
	}
	return nil
}

func (e EnvironmentIdentity) Validate() error {
	if err := requiredString(e.ID, "environment.id", MaxIDLength); err != nil {
		return err
	}
	if err := validateID(e.Provider, "environment.provider"); err != nil {
		return err
	}
	if !ValidDigest(e.ProfileDigest) {
		return invalid("environment.profile_digest", "must be a sha256 digest")
	}
	return nil
}

func (f VerificationFailure) Validate() error {
	if !f.Class.Valid() {
		return unsupported(string(f.Class), "verification.failure.class")
	}
	if err := validateID(f.Component, "verification.failure.component"); err != nil {
		return err
	}
	if err := validateID(f.Reason, "verification.failure.reason"); err != nil {
		return err
	}
	return requiredString(f.Message, "verification.failure.message", MaxSummaryLength)
}

// ComputePassed derives the only valid machine result for fully completed
// phases. Assertion false is a business result; malformed or incomplete phase
// data is a protocol error and therefore an ArtifactFailure.
func ComputePassed(plan ValidationPlan, phases []PhaseResult) (bool, error) {
	return computePassed(plan.Phases, phases)
}

// ComputeSpecPassed includes the reserved initialization phase. It is the
// machine result used by VerificationReport, ensuring that every mutating
// action has exactly one result in the immutable phase tree.
func ComputeSpecPassed(spec RunnableSpec, phases []PhaseResult) (bool, error) {
	expected := make([]ValidationPhase, 0, len(spec.ValidationPlan.Phases)+1)
	expected = append(expected, ValidationPhase{ID: "initialization", Actions: spec.Initialization})
	expected = append(expected, spec.ValidationPlan.Phases...)
	return computePassed(expected, phases)
}

func computePassed(expectedPhases []ValidationPhase, phases []PhaseResult) (bool, error) {
	if len(phases) != len(expectedPhases) {
		return false, NewArtifactFailure("phase-coverage", "verification report does not cover every validation phase")
	}
	passed := true
	for phaseIndex, specPhase := range expectedPhases {
		result := phases[phaseIndex]
		if result.ID != specPhase.ID {
			return false, NewArtifactFailure("phase-order", "verification report phase order does not match validation plan")
		}
		if len(result.Actions) != len(specPhase.Actions) || len(result.Assertions) != len(specPhase.Assertions) {
			return false, NewArtifactFailure("result-coverage", "verification report does not cover every declared result")
		}
		for actionIndex, action := range specPhase.Actions {
			item := result.Actions[actionIndex]
			if item.ID != action.ID {
				return false, NewArtifactFailure("action-order", "verification report action order does not match validation plan")
			}
			if err := item.Validate(); err != nil {
				return false, NewArtifactFailure("action-result", err.Error())
			}
			if !slices.Contains(action.ExpectedExitCodes, item.ExitCode) {
				return false, NewArtifactFailure("action-exit", "action exit result does not satisfy its contract")
			}
		}
		for assertionIndex, assertion := range specPhase.Assertions {
			item := result.Assertions[assertionIndex]
			if item.ID != assertion.ID {
				return false, NewArtifactFailure("assertion-order", "verification report assertion order does not match validation plan")
			}
			if err := item.Validate(); err != nil {
				return false, NewArtifactFailure("assertion-result", err.Error())
			}
			passed = passed && item.Satisfied
		}
	}
	return passed, nil
}

func (r ActionResult) Validate() error {
	if err := validateID(r.ID, "verification.action.id"); err != nil {
		return err
	}
	if err := requiredString(r.Summary, "verification.action.summary", MaxSummaryLength); err != nil {
		return err
	}
	if len(r.Outputs) > MaxOutputReferenceCount {
		return invalid("verification.action.outputs", "exceeds the output reference limit")
	}
	for _, output := range r.Outputs {
		if err := output.Validate("verification.action.outputs"); err != nil {
			return err
		}
	}
	return nil
}

func (r AssertionResult) Validate() error {
	if err := validateID(r.ID, "verification.assertion.id"); err != nil {
		return err
	}
	if err := requiredString(r.Summary, "verification.assertion.summary", MaxSummaryLength); err != nil {
		return err
	}
	if len(r.Details) > MaxDetailsLength || len(r.Outputs) > MaxOutputReferenceCount {
		return invalid("verification.assertion", "exceeds output limits")
	}
	for _, output := range r.Outputs {
		if err := output.Validate("verification.assertion.outputs"); err != nil {
			return err
		}
	}
	return nil
}

func (r VerificationReport) Validate(revision RunnableRevision) error {
	if r.FormatVersion != FormatVersion {
		return unsupported(r.FormatVersion, "verification.format_version")
	}
	if err := revision.Validate(); err != nil {
		return fmt.Errorf("verification report has invalid runnable revision: %w", err)
	}
	revisionDigest, err := revision.Digest()
	if err != nil {
		return fmt.Errorf("verification report revision digest: %w", err)
	}
	if r.RunnableRevisionDigest != revisionDigest {
		return errors.New("verification report is bound to another runnable revision")
	}
	if err := r.Environment.Validate(); err != nil {
		return err
	}
	profileDigest, err := revision.Spec.RuntimeProfile.Digest()
	if err != nil {
		return fmt.Errorf("verification report profile digest: %w", err)
	}
	if r.Environment.ProfileDigest != profileDigest {
		return errors.New("verification report environment is bound to another runtime profile")
	}
	if r.Attempt <= 0 || r.CreatedAt.IsZero() {
		return invalid("verification", "requires a positive attempt and creation time")
	}
	computed, computeErr := ComputeSpecPassed(revision.Spec, r.Phases)
	if r.Failure == nil {
		if computeErr != nil {
			return computeErr
		}
		if r.Passed != computed {
			return errors.New("verification report passed does not match machine result")
		}
		return nil
	}
	if err := r.Failure.Validate(); err != nil {
		return err
	}
	if r.Passed {
		return errors.New("failed verification report cannot pass")
	}
	if r.Failure.Class == FailureArtifact && computeErr == nil && computed {
		return errors.New("artifact failure conflicts with successful phase results")
	}
	if r.Failure.Class == FailureInfrastructure && len(r.Phases) > len(revision.Spec.ValidationPlan.Phases)+1 {
		return errors.New("infrastructure failure reports too many phases")
	}
	return nil
}
