package documentpractice

import (
	"bytes"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/content/workspacearchive"
	domain "github.com/breakfix/breakfix/internal/domain/documentpractice"
	"github.com/breakfix/breakfix/internal/domain/runnable"
)

func TestCompileCandidateFreezesGeneratedFilesAndApprovedProfile(t *testing.T) {
	now := time.Date(2026, 9, 15, 16, 0, 0, 0, time.UTC)
	plan := validPlan()
	plan.CreatedAt = now
	seed := serviceCandidate(t, plan, []byte("seed archive"), now)
	blueprint := CandidateBlueprint{ID: "pod-lifecycle-generated", Revision: 1, PlanID: plan.ID, PlanRevision: plan.Revision, UserSteps: plan.UserSteps, Observations: plan.Observations, Initialization: seed.Spec.Initialization, ValidationPlan: seed.Spec.ValidationPlan, Files: []GeneratedFile{
		{Path: "scripts/init.sh", Content: "#!/bin/sh\nexit 0\n", Executable: true},
		{Path: "scripts/apply.sh", Content: "#!/bin/sh\nexit 0\n", Executable: true},
		{Path: "scripts/assert.sh", Content: "#!/bin/sh\nprintf '{\"assertions\":[{\"id\":\"pod-running\",\"satisfied\":true,\"summary\":\"running\"}]}'\n", Executable: true},
	}}
	blueprint.Initialization[0].Entrypoint = "scripts/init.sh"
	blueprint.ValidationPlan.Phases[0].Actions[0].Entrypoint = "scripts/apply.sh"
	blueprint.ValidationPlan.Phases[0].Assertions[0].Entrypoint = "scripts/assert.sh"

	first, firstArchive, err := CompileCandidate(plan, seed.Spec.RuntimeProfile, seed.Spec.LifecyclePolicy, blueprint, now)
	if err != nil {
		t.Fatal(err)
	}
	second, secondArchive, err := CompileCandidate(plan, seed.Spec.RuntimeProfile, seed.Spec.LifecyclePolicy, blueprint, now)
	if err != nil {
		t.Fatal(err)
	}
	if first.Source != first.Spec.Source || first.Source != second.Source || !bytes.Equal(firstArchive, secondArchive) {
		t.Fatalf("candidate source was not deterministic: %#v %#v", first.Source, second.Source)
	}
	entries, err := workspacearchive.Decode(firstArchive)
	if err != nil || len(entries) != 4 { // Three files plus the scripts directory.
		t.Fatalf("generated archive = %#v, %v", entries, err)
	}
	blueprint.Files[0].Path = "../escape.sh"
	if _, _, err := CompileCandidate(plan, seed.Spec.RuntimeProfile, seed.Spec.LifecyclePolicy, blueprint, now); err == nil {
		t.Fatal("unsafe generated source path was accepted")
	}
}

func TestCompileCandidateRejectsChangedApprovedObservations(t *testing.T) {
	now := time.Date(2026, 9, 15, 16, 0, 0, 0, time.UTC)
	plan := validPlan()
	plan.CreatedAt = now
	seed := serviceCandidate(t, plan, []byte("seed archive"), now)
	blueprint := CandidateBlueprint{ID: "pod-lifecycle-generated", Revision: 1, PlanID: plan.ID, PlanRevision: plan.Revision, Initialization: seed.Spec.Initialization, ValidationPlan: seed.Spec.ValidationPlan, Observations: nil, Files: []GeneratedFile{{Path: "scripts/init.sh", Content: "#!/bin/sh\n", Executable: true}}}
	if _, _, err := CompileCandidate(plan, seed.Spec.RuntimeProfile, seed.Spec.LifecyclePolicy, blueprint, now); err == nil {
		t.Fatal("generator changed approved observations")
	}
}

func TestCompileCandidateRejectsGeneratorCapabilityExpansion(t *testing.T) {
	now := time.Date(2026, 9, 15, 16, 0, 0, 0, time.UTC)
	plan := validPlan()
	plan.CreatedAt = now
	plan.UserSteps = []domain.UserStep{{ID: "apply-pod", Instruction: "Apply the Pod", EvidenceIDs: []string{"page"}}}
	seed := serviceCandidate(t, plan, []byte("seed archive"), now)
	blueprint := CandidateBlueprint{ID: "pod-lifecycle-generated", Revision: 1, PlanID: plan.ID, PlanRevision: plan.Revision, UserSteps: plan.UserSteps, Observations: plan.Observations, Initialization: seed.Spec.Initialization, ValidationPlan: seed.Spec.ValidationPlan, Files: []GeneratedFile{
		{Path: "scripts/init.sh", Content: "#!/bin/sh\nexit 0\n", Executable: true},
		{Path: "scripts/apply.sh", Content: "#!/bin/sh\nexit 0\n", Executable: true},
		{Path: "scripts/assert.sh", Content: "#!/bin/sh\nprintf '{\"assertions\":[{\"id\":\"pod-running\",\"satisfied\":true,\"summary\":\"running\"}]}'\n", Executable: true},
	}}
	blueprint.Initialization[0].Entrypoint = "scripts/init.sh"
	blueprint.ValidationPlan.Phases[0].Actions[0].Entrypoint = "scripts/apply.sh"
	blueprint.ValidationPlan.Phases[0].Assertions[0].Entrypoint = "scripts/assert.sh"

	for name, mutate := range map[string]func(*CandidateBlueprint){
		"changed user step": func(value *CandidateBlueprint) { value.UserSteps[0].Instruction = "read credentials" },
		"write action on read-only boundary": func(value *CandidateBlueprint) {
			value.ValidationPlan.Phases[0].Actions[0].BoundaryID = "management-read"
		},
		"assertion on write boundary": func(value *CandidateBlueprint) {
			value.ValidationPlan.Phases[0].Assertions[0].BoundaryID = "management-write"
		},
		"unknown execution boundary": func(value *CandidateBlueprint) { value.Initialization[0].BoundaryID = "public-network" },
	} {
		t.Run(name, func(t *testing.T) {
			value := blueprint
			value.UserSteps = append([]domain.UserStep(nil), blueprint.UserSteps...)
			value.Initialization = append([]runnable.ActionSpec(nil), blueprint.Initialization...)
			value.ValidationPlan.Phases = append([]runnable.ValidationPhase(nil), blueprint.ValidationPlan.Phases...)
			value.ValidationPlan.Phases[0].Actions = append([]runnable.ActionSpec(nil), blueprint.ValidationPlan.Phases[0].Actions...)
			value.ValidationPlan.Phases[0].Assertions = append([]runnable.AssertionSpec(nil), blueprint.ValidationPlan.Phases[0].Assertions...)
			mutate(&value)
			if _, _, err := CompileCandidate(plan, seed.Spec.RuntimeProfile, seed.Spec.LifecyclePolicy, value, now); err == nil {
				t.Fatal("capability expansion was accepted")
			}
		})
	}
}
