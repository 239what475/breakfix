package environment

import (
	"time"

	"github.com/breakfix/breakfix/internal/domain/checkpoint"
)

type RecordedCheckpointResult struct {
	checkpoint.Result
	FirstPassedAt *time.Time
}

type CheckpointStatus struct {
	Results   []RecordedCheckpointResult
	CheckedAt *time.Time
	Error     string
}

// RecordCheckpointStatus preserves the first successful time for each
// checkpoint across polling, controller restarts, and later failures.
func RecordCheckpointStatus(previous *CheckpointStatus, report checkpoint.Report, checkErr error, now time.Time) (*CheckpointStatus, bool) {
	next := &CheckpointStatus{}
	if checkErr != nil {
		next.Error = truncate(checkErr.Error(), 4000)
		if previous != nil {
			next.Results = append([]RecordedCheckpointResult(nil), previous.Results...)
		}
	} else {
		firstPassed := make(map[string]*time.Time)
		if previous != nil {
			for _, result := range previous.Results {
				if result.FirstPassedAt != nil {
					firstPassed[result.ID] = result.FirstPassedAt
				}
			}
		}
		next.Results = make([]RecordedCheckpointResult, 0, len(report.Checks))
		for _, result := range report.Checks {
			recorded := RecordedCheckpointResult{Result: result}
			if first := firstPassed[result.ID]; first != nil {
				recorded.FirstPassedAt = copyTime(first)
			} else if result.Passed {
				at := now.UTC()
				recorded.FirstPassedAt = &at
			}
			next.Results = append(next.Results, recorded)
		}
	}
	if equalCheckpointStatus(previous, next) {
		return previous, false
	}
	checked := now.UTC()
	next.CheckedAt = &checked
	return next, true
}

func equalCheckpointStatus(left, right *CheckpointStatus) bool {
	if left == nil || right == nil {
		return left == right
	}
	if left.Error != right.Error || len(left.Results) != len(right.Results) {
		return false
	}
	for index := range left.Results {
		leftResult := left.Results[index]
		rightResult := right.Results[index]
		if leftResult.Result != rightResult.Result || !equalTime(leftResult.FirstPassedAt, rightResult.FirstPassedAt) {
			return false
		}
	}
	return true
}

func equalTime(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == right
	}
	return left.Equal(*right)
}

func copyTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := value.UTC()
	return &copy
}

func truncate(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
