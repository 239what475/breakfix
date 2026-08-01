package controller

import (
	"testing"
	"time"

	breakfixv1 "github.com/breakfix/breakfix/api/v1"
)

func TestRecordCheckpointStatusPreservesFirstPassAcrossPollsAndRestart(t *testing.T) {
	status := &breakfixv1.EnvironmentStatus{}
	now := time.Date(2020, 7, 30, 8, 0, 0, 0, time.UTC)
	failed := []breakfixv1.CheckpointResultStatus{{ID: "repair", Passed: false, Summary: "not ready"}}
	recordRuntimeCheckpointStatus(status, failed, nil, now)
	if status.Checkpoints.Results[0].FirstPassedAt != nil {
		t.Fatalf("failed checkpoint recorded first pass: %#v", status.Checkpoints.Results[0])
	}

	passed := []breakfixv1.CheckpointResultStatus{{ID: "repair", Passed: true, Summary: "ready"}}
	recordRuntimeCheckpointStatus(status, passed, nil, now.Add(time.Second))
	first := status.Checkpoints.Results[0].FirstPassedAt
	if first == nil || first.IsZero() {
		t.Fatalf("first pass timestamp missing: %#v", status.Checkpoints.Results[0])
	}

	// A controller restart receives the persisted CRD status and must preserve
	// the original timestamp rather than assigning the next reconcile time.
	restarted := status.DeepCopy()
	checkedAt := restarted.Checkpoints.CheckedAt
	recordRuntimeCheckpointStatus(restarted, passed, nil, now.Add(2*time.Second))
	if restarted.Checkpoints.CheckedAt != checkedAt {
		t.Fatal("unchanged successful result unexpectedly updated status")
	}
	if got := restarted.Checkpoints.Results[0].FirstPassedAt; got == nil || !got.Equal(first) {
		t.Fatalf("first pass timestamp changed after restart: got %v want %v", got, first)
	}

	recordRuntimeCheckpointStatus(restarted, failed, nil, now.Add(3*time.Second))
	if got := restarted.Checkpoints.Results[0].FirstPassedAt; got == nil || !got.Equal(first) {
		t.Fatalf("later failure lost first pass: got %v want %v", got, first)
	}
	recordRuntimeCheckpointStatus(restarted, passed, nil, now.Add(4*time.Second))
	if got := restarted.Checkpoints.Results[0].FirstPassedAt; got == nil || !got.Equal(first) {
		t.Fatalf("later recovery changed first pass: got %v want %v", got, first)
	}
	if first.After(time.Now().UTC().Add(time.Second)) {
		t.Fatalf("first pass timestamp is in the future: %s", first)
	}
}

func TestRecordCheckpointStatusKeepsFirstPassThroughRunnerError(t *testing.T) {
	status := &breakfixv1.EnvironmentStatus{}
	now := time.Date(2020, 7, 30, 8, 0, 0, 0, time.UTC)
	passed := []breakfixv1.CheckpointResultStatus{{ID: "repair", Passed: true, Summary: "ready"}}
	recordRuntimeCheckpointStatus(status, passed, nil, now)
	first := status.Checkpoints.Results[0].FirstPassedAt
	if first == nil {
		t.Fatal("initial pass did not set first pass time")
	}
	recordRuntimeCheckpointStatus(status, nil, assertCheckpointError{}, now.Add(time.Second))
	if got := status.Checkpoints.Results[0].FirstPassedAt; got == nil || !got.Equal(first) {
		t.Fatalf("runner error lost first pass: got %v want %v", got, first)
	}
	recordRuntimeCheckpointStatus(status, passed, nil, now.Add(2*time.Second))
	if got := status.Checkpoints.Results[0].FirstPassedAt; got == nil || !got.Equal(first) {
		t.Fatalf("runner recovery changed first pass: got %v want %v", got, first)
	}
}

type assertCheckpointError struct{}

func (assertCheckpointError) Error() string { return "checkpoint runner unavailable" }
