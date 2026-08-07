package environment

import (
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/domain/checkpoint"
)

func TestRecordCheckpointStatusPreservesFirstPassAcrossPollsAndRestart(t *testing.T) {
	now := time.Date(2020, 7, 30, 8, 0, 0, 0, time.UTC)
	failed := checkpoint.Report{Checks: []checkpoint.Result{{ID: "repair", Passed: false, Summary: "not ready"}}}
	status, changed := RecordCheckpointStatus(nil, failed, nil, now)
	if !changed {
		t.Fatal("initial checkpoint status was not recorded")
	}
	if status.Results[0].FirstPassedAt != nil {
		t.Fatalf("failed checkpoint recorded first pass: %#v", status.Results[0])
	}

	passed := checkpoint.Report{Checks: []checkpoint.Result{{ID: "repair", Passed: true, Summary: "ready"}}}
	status, changed = RecordCheckpointStatus(status, passed, nil, now.Add(time.Second))
	if !changed {
		t.Fatal("passing checkpoint status was not recorded")
	}
	first := status.Results[0].FirstPassedAt
	if first == nil || first.IsZero() {
		t.Fatalf("first pass timestamp missing: %#v", status.Results[0])
	}

	// A restarted reconciler receives the persisted status and must preserve
	// the original timestamp rather than assigning the next poll time.
	restarted := checkpointStatusCopy(status)
	checkedAt := *restarted.CheckedAt
	restarted, changed = RecordCheckpointStatus(restarted, passed, nil, now.Add(2*time.Second))
	if changed || !restarted.CheckedAt.Equal(checkedAt) {
		t.Fatal("unchanged successful result unexpectedly updated status")
	}
	if got := restarted.Results[0].FirstPassedAt; got == nil || !got.Equal(*first) {
		t.Fatalf("first pass timestamp changed after restart: got %v want %v", got, first)
	}

	restarted, _ = RecordCheckpointStatus(restarted, failed, nil, now.Add(3*time.Second))
	if got := restarted.Results[0].FirstPassedAt; got == nil || !got.Equal(*first) {
		t.Fatalf("later failure lost first pass: got %v want %v", got, first)
	}
	restarted, _ = RecordCheckpointStatus(restarted, passed, nil, now.Add(4*time.Second))
	if got := restarted.Results[0].FirstPassedAt; got == nil || !got.Equal(*first) {
		t.Fatalf("later recovery changed first pass: got %v want %v", got, first)
	}
}

func TestRecordCheckpointStatusKeepsFirstPassThroughRunnerError(t *testing.T) {
	now := time.Date(2020, 7, 30, 8, 0, 0, 0, time.UTC)
	passed := checkpoint.Report{Checks: []checkpoint.Result{{ID: "repair", Passed: true, Summary: "ready"}}}
	status, _ := RecordCheckpointStatus(nil, passed, nil, now)
	first := status.Results[0].FirstPassedAt
	if first == nil {
		t.Fatal("initial pass did not set first pass time")
	}
	status, _ = RecordCheckpointStatus(status, checkpoint.Report{}, assertCheckpointError{}, now.Add(time.Second))
	if got := status.Results[0].FirstPassedAt; got == nil || !got.Equal(*first) {
		t.Fatalf("runner error lost first pass: got %v want %v", got, first)
	}
	status, _ = RecordCheckpointStatus(status, passed, nil, now.Add(2*time.Second))
	if got := status.Results[0].FirstPassedAt; got == nil || !got.Equal(*first) {
		t.Fatalf("runner recovery changed first pass: got %v want %v", got, first)
	}
}

type assertCheckpointError struct{}

func (assertCheckpointError) Error() string { return "checkpoint runner unavailable" }

func checkpointStatusCopy(status *CheckpointStatus) *CheckpointStatus {
	if status == nil {
		return nil
	}
	copy := &CheckpointStatus{Error: status.Error, Results: make([]RecordedCheckpointResult, len(status.Results))}
	if status.CheckedAt != nil {
		checked := *status.CheckedAt
		copy.CheckedAt = &checked
	}
	for index, result := range status.Results {
		copy.Results[index] = result
		if result.FirstPassedAt != nil {
			first := *result.FirstPassedAt
			copy.Results[index].FirstPassedAt = &first
		}
	}
	return copy
}
