package documentpractice

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/content/workspacearchive"
	domain "github.com/breakfix/breakfix/internal/domain/documentpractice"
	"github.com/breakfix/breakfix/internal/domain/runnable"
)

const (
	maxGeneratedSourceFiles = 128
	maxGeneratedFileBytes   = 256 * 1024
)

// GeneratedFile is the only file-level output accepted from a documentation
// generator. The Server archives these files deterministically; an Agent never
// supplies an archive, source digest, provider reference, or mutable path.
type GeneratedFile struct {
	Path       string `json:"path" jsonschema:"required"`
	Content    string `json:"content" jsonschema:"required"`
	Executable bool   `json:"executable"`
}

func (f GeneratedFile) Validate() error {
	if err := domain.ValidateRelativePath(f.Path); err != nil {
		return fmt.Errorf("generated file path: %w", err)
	}
	if len(f.Content) > maxGeneratedFileBytes {
		return errors.New("generated file exceeds the source file limit")
	}
	if strings.ContainsRune(f.Content, '\x00') {
		return errors.New("generated file contains a NUL byte")
	}
	return nil
}

// CandidateBlueprint is a generator Agent's bounded structured result. The
// approved runtime profile, source identity, content identity, and digest are
// assembled by Server-owned code after this value validates.
type CandidateBlueprint struct {
	ID              string                    `json:"id" jsonschema:"required"`
	Revision        int64                     `json:"revision" jsonschema:"required"`
	PlanID          string                    `json:"plan_id" jsonschema:"required"`
	PlanRevision    int64                     `json:"plan_revision" jsonschema:"required"`
	Files           []GeneratedFile           `json:"files" jsonschema:"required"`
	UserSteps       []domain.UserStep         `json:"user_steps,omitempty"`
	Observations    []domain.ObservationPoint `json:"observations,omitempty"`
	Initialization  []runnable.ActionSpec     `json:"initialization" jsonschema:"required"`
	ValidationPlan  runnable.ValidationPlan   `json:"validation_plan" jsonschema:"required"`
	LifecyclePolicy runnable.LifecyclePolicy  `json:"lifecycle_policy" jsonschema:"required"`
}

func (b CandidateBlueprint) Validate(plan domain.LearningUnitPlan, profile runnable.RuntimeProfile) error {
	if err := plan.Validate(); err != nil {
		return err
	}
	if plan.NoPractice {
		return errors.New("no_practice plan cannot produce a candidate blueprint")
	}
	if err := stableCandidateID(b.ID); err != nil || b.Revision < 1 || b.PlanID != plan.ID || b.PlanRevision != plan.Revision {
		return errors.New("candidate blueprint identity is invalid")
	}
	if len(b.Files) == 0 || len(b.Files) > maxGeneratedSourceFiles {
		return errors.New("candidate blueprint has an invalid file count")
	}
	paths := make(map[string]struct{}, len(b.Files))
	for _, file := range b.Files {
		if err := file.Validate(); err != nil {
			return err
		}
		if _, exists := paths[file.Path]; exists {
			return errors.New("candidate blueprint repeats a file path")
		}
		paths[file.Path] = struct{}{}
	}
	if !reflect.DeepEqual(b.UserSteps, plan.UserSteps) || !reflect.DeepEqual(b.Observations, plan.Observations) {
		return errors.New("candidate blueprint changed approved user steps or observations")
	}
	if err := profileMatchesPlan(profile, plan); err != nil {
		return err
	}
	spec := runnable.RunnableSpec{FormatVersion: runnable.FormatVersion, Identity: runnable.ContentIdentity{Kind: "documentation-practice", ID: domain.ContentID(plan.Context), Revision: b.ID}, RuntimeProfile: profile, Source: runnable.SourceArchive{FormatVersion: runnable.FormatVersion, Reference: "runnable-source://pending", Digest: "sha256:" + strings.Repeat("0", 64)}, Initialization: b.Initialization, ValidationPlan: b.ValidationPlan, LifecyclePolicy: b.LifecyclePolicy}
	return spec.Validate()
}

// CompileCandidate freezes a generated blueprint to the public source archive
// and runtime contract. It performs no provider action and can be retried
// safely because workspacearchive.Encode is deterministic.
func CompileCandidate(plan domain.LearningUnitPlan, profile runnable.RuntimeProfile, blueprint CandidateBlueprint, now time.Time) (domain.PracticeCandidate, []byte, error) {
	if now.IsZero() {
		return domain.PracticeCandidate{}, nil, errors.New("candidate compilation time is required")
	}
	if err := blueprint.Validate(plan, profile); err != nil {
		return domain.PracticeCandidate{}, nil, err
	}
	entries := make([]workspacearchive.Entry, 0, len(blueprint.Files))
	for _, file := range blueprint.Files {
		mode := 0o644
		if file.Executable {
			mode = 0o755
		}
		entries = append(entries, workspacearchive.Entry{Path: file.Path, Mode: mode, Content: []byte(file.Content)})
	}
	archive, err := workspacearchive.Encode(entries)
	if err != nil {
		return domain.PracticeCandidate{}, nil, fmt.Errorf("encode generated candidate archive: %w", err)
	}
	if len(archive) > runnable.MaxSourceArchiveBytes {
		return domain.PracticeCandidate{}, nil, errors.New("generated candidate archive exceeds platform limit")
	}
	sum := sha256.Sum256(archive)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	source := runnable.SourceArchive{FormatVersion: runnable.FormatVersion, Reference: "runnable-source://sha256/" + hex.EncodeToString(sum[:]), Digest: digest}
	spec := runnable.RunnableSpec{FormatVersion: runnable.FormatVersion, Identity: runnable.ContentIdentity{Kind: "documentation-practice", ID: domain.ContentID(plan.Context), Revision: blueprint.ID}, RuntimeProfile: profile, Source: source, Initialization: blueprint.Initialization, ValidationPlan: blueprint.ValidationPlan, LifecyclePolicy: blueprint.LifecyclePolicy}
	candidate := domain.PracticeCandidate{FormatVersion: domain.FormatVersion, ID: blueprint.ID, Revision: blueprint.Revision, PlanID: blueprint.PlanID, PlanRevision: blueprint.PlanRevision, Context: plan.Context, Source: source, Spec: spec, UserSteps: append([]domain.UserStep(nil), blueprint.UserSteps...), Observations: append([]domain.ObservationPoint(nil), blueprint.Observations...), CreatedAt: now.UTC()}
	if err := ValidateCandidateAgainstPlan(candidate, plan); err != nil {
		return domain.PracticeCandidate{}, nil, err
	}
	return candidate, archive, nil
}

func profileMatchesPlan(profile runnable.RuntimeProfile, plan domain.LearningUnitPlan) error {
	if err := profile.Validate(); err != nil {
		return err
	}
	if profile.Runtime != plan.Runtime.Runtime || profile.BaseImage != plan.Runtime.BaseImage || profile.Resources != plan.Runtime.Resources || profile.Network != plan.Runtime.Network || profile.Topology != plan.Runtime.Topology {
		return errors.New("resolved runtime profile differs from the approved plan")
	}
	return nil
}

func stableCandidateID(value string) error {
	if len(value) == 0 || len(value) > 128 {
		return errors.New("candidate blueprint id is invalid")
	}
	for _, character := range value {
		if !(character == '-' || character >= 'a' && character <= 'z' || character >= '0' && character <= '9') {
			return errors.New("candidate blueprint id is invalid")
		}
	}
	return nil
}
