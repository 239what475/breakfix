package controller

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	breakfixv1 "github.com/breakfix/breakfix/internal/k8s/apis/breakfix/v1"
)

type checkpointReport struct {
	Checks []checkpointResult `json:"checks"`
}

type checkpointResult struct {
	ID      string `json:"id"`
	Passed  bool   `json:"passed"`
	Summary string `json:"summary"`
	Details string `json:"details"`
}

func parseCheckpointReport(raw string, expectedIDs []string) ([]breakfixv1.CheckpointResultStatus, error) {
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
	results := make([]breakfixv1.CheckpointResultStatus, 0, len(report.Checks))
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
		results = append(results, breakfixv1.CheckpointResultStatus{
			ID:      check.ID,
			Passed:  check.Passed,
			Summary: check.Summary,
			Details: check.Details,
		})
	}
	for id := range expected {
		if _, ok := seen[id]; !ok {
			return nil, fmt.Errorf("checkpoint report is missing id %q", id)
		}
	}
	return results, nil
}

func checkpointsPassed(results []breakfixv1.CheckpointResultStatus) bool {
	return len(results) > 0 && !slices.ContainsFunc(results, func(result breakfixv1.CheckpointResultStatus) bool {
		return !result.Passed
	})
}
