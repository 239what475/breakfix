package documentpractice

import (
	"bytes"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/content/workspacearchive"
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
