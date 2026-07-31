package challenge

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// CheckResult is one current-state result emitted by a runtime checks.sh.
type CheckResult struct {
	ID      string `json:"id"`
	Passed  bool   `json:"passed"`
	Summary string `json:"summary"`
	Details string `json:"details"`
}

// CheckReport is the only completion result for a challenge. The challenge is
// complete when every declared checkpoint in this report passes.
type CheckReport struct {
	Checks []CheckResult `json:"checks"`
}

func (r CheckReport) Passed() bool {
	return len(r.Checks) > 0 && !slices.ContainsFunc(r.Checks, func(check CheckResult) bool {
		return !check.Passed
	})
}

func ParseCheckReport(raw string, checkpoints []Checkpoint) (*CheckReport, error) {
	var report CheckReport
	if err := json.Unmarshal([]byte(raw), &report); err != nil {
		return nil, fmt.Errorf("parse checkpoint report: %w", err)
	}
	if len(report.Checks) == 0 {
		return nil, fmt.Errorf("checkpoint report contains no checks")
	}

	expected := make(map[string]Checkpoint, len(checkpoints))
	for _, checkpoint := range checkpoints {
		expected[checkpoint.ID] = checkpoint
	}
	seen := make(map[string]struct{}, len(report.Checks))
	for i := range report.Checks {
		check := &report.Checks[i]
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
	}
	for id := range expected {
		if _, ok := seen[id]; !ok {
			return nil, fmt.Errorf("checkpoint report is missing id %q", id)
		}
	}
	return &report, nil
}

type Content struct {
	Problem  string            `json:"problem"`
	Solution string            `json:"solution"`
	Hints    map[string]string `json:"hints"`
}

func ReadContent(entry *Entry) (*Content, error) {
	if entry == nil {
		return nil, fmt.Errorf("challenge is nil")
	}
	problem, err := os.ReadFile(filepath.Join(entry.Dir, "problem.md"))
	if err != nil {
		return nil, fmt.Errorf("read problem.md: %w", err)
	}
	solution, err := os.ReadFile(filepath.Join(entry.Dir, "solution.md"))
	if err != nil {
		return nil, fmt.Errorf("read solution.md: %w", err)
	}
	content := &Content{
		Problem:  string(problem),
		Solution: string(solution),
		Hints:    make(map[string]string),
	}
	for _, checkpoint := range entry.Checkpoints {
		if checkpoint.Hint == "" {
			continue
		}
		path, err := safeChallengePath(entry.Dir, checkpoint.Hint)
		if err != nil {
			return nil, fmt.Errorf("checkpoint %q hint: %w", checkpoint.ID, err)
		}
		hint, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read checkpoint %q hint: %w", checkpoint.ID, err)
		}
		content.Hints[checkpoint.ID] = string(hint)
	}
	return content, nil
}

func safeChallengePath(root, name string) (string, error) {
	if strings.TrimSpace(name) == "" || filepath.IsAbs(name) {
		return "", fmt.Errorf("must be a non-empty relative path")
	}
	path := filepath.Clean(filepath.Join(root, name))
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("must remain inside the challenge directory")
	}
	return path, nil
}
