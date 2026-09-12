package authoring

import "testing"

func TestPlanAllowsGenerationWithoutOverviewOrCheckpoints(t *testing.T) {
	plan := Plan{Metadata: Metadata{Title: "复现 DNS 故障", Description: "仅验证故障现象。", Runtime: "node"}}
	if err := plan.ValidateForGeneration(); err != nil {
		t.Fatalf("ValidateForGeneration() error = %v", err)
	}
}

func TestPlanStillValidatesProvidedCheckpoint(t *testing.T) {
	plan := Plan{
		Metadata:    Metadata{Title: "复现 DNS 故障", Description: "仅验证故障现象。", Runtime: "node"},
		Checkpoints: []Checkpoint{{ID: "dns-ready", Title: "DNS ready"}},
	}
	if err := plan.ValidateForGeneration(); err == nil {
		t.Fatal("expected incomplete optional checkpoint to be rejected")
	}
}

func TestStageOperationAndGeneratedCheckpointIDAreDeterministic(t *testing.T) {
	arguments := struct {
		ID       string `json:"id"`
		Markdown string `json:"markdown"`
	}{Markdown: "service is ready"}
	first, err := NewStageOperation("run-one", 3, "checkpoint", arguments)
	if err != nil {
		t.Fatalf("create stage operation: %v", err)
	}
	second, err := NewStageOperation("run-one", 3, "checkpoint", arguments)
	if err != nil {
		t.Fatalf("recreate stage operation: %v", err)
	}
	if first != second || CheckpointIDForOperation(first) != CheckpointIDForOperation(second) {
		t.Fatalf("operation identity changed across replay: first=%#v second=%#v", first, second)
	}
	changed, err := NewStageOperation("run-one", 3, "checkpoint", struct {
		ID       string `json:"id"`
		Markdown string `json:"markdown"`
	}{Markdown: "another state"})
	if err != nil {
		t.Fatalf("create changed stage operation: %v", err)
	}
	if changed.ID == first.ID {
		t.Fatal("different Plan arguments reused the same operation identity")
	}
}
