// Package reproduction defines the reproduce.sh evidence protocol used to
// prove an operations scenario's initial phenomenon before any repair runs.
package reproduction

import (
	"encoding/json"
	"fmt"
	"strings"
)

type Evidence struct {
	ID       string `json:"id"`
	Observed bool   `json:"observed"`
	Summary  string `json:"summary"`
	Details  string `json:"details,omitempty"`
}

type Report struct {
	Evidence []Evidence `json:"evidence"`
}

func (r Report) Observed() bool {
	if len(r.Evidence) == 0 {
		return false
	}
	for _, evidence := range r.Evidence {
		if !evidence.Observed {
			return false
		}
	}
	return true
}

// Parse validates one reproduce.sh JSON document against the evidence IDs
// assigned to that execution location. An observed=false result is a valid
// report proving that the target phenomenon was not reproduced.
func Parse(raw string, expectedIDs []string) (Report, error) {
	expected, err := expectedSet(expectedIDs)
	if err != nil {
		return Report{}, err
	}
	var report Report
	if err := json.Unmarshal([]byte(raw), &report); err != nil {
		return Report{}, fmt.Errorf("parse reproduction report: %w", err)
	}
	if len(report.Evidence) == 0 {
		return Report{}, fmt.Errorf("reproduction report contains no evidence")
	}
	seen := make(map[string]struct{}, len(report.Evidence))
	for index := range report.Evidence {
		evidence := &report.Evidence[index]
		evidence.ID = strings.TrimSpace(evidence.ID)
		evidence.Summary = strings.TrimSpace(evidence.Summary)
		evidence.Details = strings.TrimSpace(evidence.Details)
		if _, ok := expected[evidence.ID]; !ok {
			return Report{}, fmt.Errorf("reproduction report contains unknown id %q", evidence.ID)
		}
		if _, duplicate := seen[evidence.ID]; duplicate {
			return Report{}, fmt.Errorf("reproduction report contains duplicate id %q", evidence.ID)
		}
		if evidence.Summary == "" {
			return Report{}, fmt.Errorf("reproduction report has empty summary for %q", evidence.ID)
		}
		seen[evidence.ID] = struct{}{}
	}
	for id := range expected {
		if _, ok := seen[id]; !ok {
			return Report{}, fmt.Errorf("reproduction report is missing id %q", id)
		}
	}
	return report, nil
}

func expectedSet(expectedIDs []string) (map[string]struct{}, error) {
	expected := make(map[string]struct{}, len(expectedIDs))
	for _, rawID := range expectedIDs {
		id := strings.TrimSpace(rawID)
		if id == "" {
			return nil, fmt.Errorf("expected reproduction evidence id is empty")
		}
		if _, exists := expected[id]; exists {
			return nil, fmt.Errorf("expected reproduction evidence id %q is duplicated", id)
		}
		expected[id] = struct{}{}
	}
	if len(expected) == 0 {
		return nil, fmt.Errorf("no reproduction evidence ids are expected")
	}
	return expected, nil
}
