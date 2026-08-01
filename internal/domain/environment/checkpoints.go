package environment

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type CheckpointResult struct {
	ID      string
	Passed  bool
	Summary string
	Details string
}

type RecordedCheckpointResult struct {
	CheckpointResult
	FirstPassedAt *time.Time
}

type CheckpointStatus struct {
	Results   []RecordedCheckpointResult
	CheckedAt *time.Time
	Error     string
}

type checkpointReport struct {
	Checks []checkpointResult `json:"checks"`
}

type checkpointResult struct {
	ID      string `json:"id"`
	Passed  bool   `json:"passed"`
	Summary string `json:"summary"`
	Details string `json:"details"`
}

func ParseCheckpointReport(raw string, expectedIDs []string) ([]CheckpointResult, error) {
	expected := make(map[string]struct{}, len(expectedIDs))
	for _, rawID := range expectedIDs {
		id := strings.TrimSpace(rawID)
		if id == "" {
			return nil, fmt.Errorf("expected checkpoint id is empty")
		}
		if _, exists := expected[id]; exists {
			return nil, fmt.Errorf("expected checkpoint id %q is duplicated", id)
		}
		expected[id] = struct{}{}
	}
	if len(expected) == 0 {
		return nil, fmt.Errorf("environment has no expected checkpoints")
	}

	var report checkpointReport
	if err := json.Unmarshal([]byte(raw), &report); err != nil {
		return nil, fmt.Errorf("parse checkpoint report: %w", err)
	}
	if len(report.Checks) == 0 {
		return nil, fmt.Errorf("checkpoint report contains no checks")
	}

	seen := make(map[string]struct{}, len(report.Checks))
	results := make([]CheckpointResult, 0, len(report.Checks))
	for _, check := range report.Checks {
		check.ID = strings.TrimSpace(check.ID)
		check.Summary = strings.TrimSpace(check.Summary)
		check.Details = strings.TrimSpace(check.Details)
		if _, ok := expected[check.ID]; !ok {
			return nil, fmt.Errorf("checkpoint report contains unknown id %q", check.ID)
		}
		if _, ok := seen[check.ID]; ok {
			return nil, fmt.Errorf("checkpoint report contains duplicate id %q", check.ID)
		}
		if check.Summary == "" {
			return nil, fmt.Errorf("checkpoint report has empty summary for %q", check.ID)
		}
		seen[check.ID] = struct{}{}
		results = append(results, CheckpointResult{
			ID: check.ID, Passed: check.Passed, Summary: check.Summary, Details: check.Details,
		})
	}
	for id := range expected {
		if _, ok := seen[id]; !ok {
			return nil, fmt.Errorf("checkpoint report is missing id %q", id)
		}
	}
	return results, nil
}

func AllCheckpointsPassed(results []CheckpointResult) bool {
	if len(results) == 0 {
		return false
	}
	for _, result := range results {
		if !result.Passed {
			return false
		}
	}
	return true
}

// RecordCheckpointStatus preserves the first successful time for each
// checkpoint across polling, controller restarts, and later failures.
func RecordCheckpointStatus(previous *CheckpointStatus, results []CheckpointResult, checkErr error, now time.Time) (*CheckpointStatus, bool) {
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
		next.Results = make([]RecordedCheckpointResult, 0, len(results))
		for _, result := range results {
			recorded := RecordedCheckpointResult{CheckpointResult: result}
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
		if leftResult.CheckpointResult != rightResult.CheckpointResult || !equalTime(leftResult.FirstPassedAt, rightResult.FirstPassedAt) {
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
