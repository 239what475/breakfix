// Package checkpoint defines the checks.sh report protocol shared by content
// verification and runtime Environment controllers.
package checkpoint

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Result is one current-state observation emitted by checks.sh.
type Result struct {
	ID      string `json:"id"`
	Passed  bool   `json:"passed"`
	Summary string `json:"summary"`
	Details string `json:"details"`
}

// Report contains exactly one result for every checkpoint expected at the
// checks.sh execution location.
type Report struct {
	Checks []Result `json:"checks"`
}

// Passed reports whether the non-empty report contains only passing results.
func (r Report) Passed() bool {
	if len(r.Checks) == 0 {
		return false
	}
	for _, result := range r.Checks {
		if !result.Passed {
			return false
		}
	}
	return true
}

// Parse validates one checks.sh JSON document against the checkpoint IDs
// assigned to that execution location. Result order follows the JSON report;
// checkpoint IDs have no semantic ordering.
func Parse(raw string, expectedIDs []string) (Report, error) {
	expected, err := expectedSet(expectedIDs)
	if err != nil {
		return Report{}, err
	}
	var report Report
	if err := json.Unmarshal([]byte(raw), &report); err != nil {
		return Report{}, fmt.Errorf("parse checkpoint report: %w", err)
	}
	if len(report.Checks) == 0 {
		return Report{}, fmt.Errorf("checkpoint report contains no checks")
	}

	seen := make(map[string]struct{}, len(report.Checks))
	for index := range report.Checks {
		result := &report.Checks[index]
		result.ID = strings.TrimSpace(result.ID)
		result.Summary = strings.TrimSpace(result.Summary)
		result.Details = strings.TrimSpace(result.Details)
		if _, ok := expected[result.ID]; !ok {
			return Report{}, fmt.Errorf("checkpoint report contains unknown id %q", result.ID)
		}
		if _, ok := seen[result.ID]; ok {
			return Report{}, fmt.Errorf("checkpoint report contains duplicate id %q", result.ID)
		}
		if result.Summary == "" {
			return Report{}, fmt.Errorf("checkpoint report has empty summary for %q", result.ID)
		}
		seen[result.ID] = struct{}{}
	}
	for id := range expected {
		if _, ok := seen[id]; !ok {
			return Report{}, fmt.Errorf("checkpoint report is missing id %q", id)
		}
	}
	return report, nil
}

func expectedSet(expectedIDs []string) (map[string]struct{}, error) {
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
		return nil, fmt.Errorf("no checkpoint ids are expected")
	}
	return expected, nil
}
