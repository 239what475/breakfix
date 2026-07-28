package controller

import (
	"testing"
	"time"

	breakfixv1 "github.com/breakfix/breakfix/internal/k8s/apis/breakfix/v1"
)

func TestRecordCheckpointStatusPreservesFirstPassAcrossPollsAndRestart(t *testing.T) {
	status := &breakfixv1.CommonEnvironmentStatus{}
	failed := []breakfixv1.CheckpointResultStatus{{ID: "repair", Passed: false, Summary: "not ready"}}
	if !recordCheckpointStatus(status, failed, nil) {
		t.Fatal("initial failed result did not update status")
	}
	if status.Checkpoints.Results[0].FirstPassedAt != nil {
		t.Fatalf("failed checkpoint recorded first pass: %#v", status.Checkpoints.Results[0])
	}

	passed := []breakfixv1.CheckpointResultStatus{{ID: "repair", Passed: true, Summary: "ready"}}
	if !recordCheckpointStatus(status, passed, nil) {
		t.Fatal("first pass did not update status")
	}
	first := status.Checkpoints.Results[0].FirstPassedAt
	if first == nil || first.IsZero() {
		t.Fatalf("first pass timestamp missing: %#v", status.Checkpoints.Results[0])
	}

	// A controller restart receives the persisted CRD status and must preserve
	// the original timestamp rather than assigning the next reconcile time.
	restarted := status.DeepCopy()
	if recordCheckpointStatus(restarted, passed, nil) {
		t.Fatal("unchanged successful result unexpectedly updated status")
	}
	if got := restarted.Checkpoints.Results[0].FirstPassedAt; got == nil || !got.Equal(first) {
		t.Fatalf("first pass timestamp changed after restart: got %v want %v", got, first)
	}

	if !recordCheckpointStatus(restarted, failed, nil) {
		t.Fatal("later failure did not update current status")
	}
	if got := restarted.Checkpoints.Results[0].FirstPassedAt; got == nil || !got.Equal(first) {
		t.Fatalf("later failure lost first pass: got %v want %v", got, first)
	}
	if !recordCheckpointStatus(restarted, passed, nil) {
		t.Fatal("later recovery did not update current status")
	}
	if got := restarted.Checkpoints.Results[0].FirstPassedAt; got == nil || !got.Equal(first) {
		t.Fatalf("later recovery changed first pass: got %v want %v", got, first)
	}
	if first.Time.After(time.Now().UTC().Add(time.Second)) {
		t.Fatalf("first pass timestamp is in the future: %s", first)
	}
}

func TestRecordCheckpointStatusKeepsFirstPassThroughRunnerError(t *testing.T) {
	status := &breakfixv1.CommonEnvironmentStatus{}
	passed := []breakfixv1.CheckpointResultStatus{{ID: "repair", Passed: true, Summary: "ready"}}
	if !recordCheckpointStatus(status, passed, nil) {
		t.Fatal("initial pass did not update status")
	}
	first := status.Checkpoints.Results[0].FirstPassedAt
	if first == nil {
		t.Fatal("initial pass did not set first pass time")
	}
	if !recordCheckpointStatus(status, nil, assertCheckpointError{}) {
		t.Fatal("runner error did not update status")
	}
	if got := status.Checkpoints.Results[0].FirstPassedAt; got == nil || !got.Equal(first) {
		t.Fatalf("runner error lost first pass: got %v want %v", got, first)
	}
	if !recordCheckpointStatus(status, passed, nil) {
		t.Fatal("recovered runner did not clear error")
	}
	if got := status.Checkpoints.Results[0].FirstPassedAt; got == nil || !got.Equal(first) {
		t.Fatalf("runner recovery changed first pass: got %v want %v", got, first)
	}
}

type assertCheckpointError struct{}

func (assertCheckpointError) Error() string { return "checkpoint runner unavailable" }
