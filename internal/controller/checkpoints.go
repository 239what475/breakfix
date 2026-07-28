package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/k8s"
	breakfixv1 "github.com/breakfix/breakfix/internal/k8s/apis/breakfix/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	checkpointInterval = 4 * time.Second
	checkpointCommand  = "/checks/checkpoints.sh"
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

// runCheckpointEvaluation uses only the immutable Environment spec. The
// controller intentionally has no access to the filesystem challenge catalog.
func runCheckpointEvaluation(ctx context.Context, client *k8s.Client, expectedIDs []string, namespace, pod string) ([]breakfixv1.CheckpointResultStatus, error) {
	checkCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	exitCode, output, err := client.ExecInPodContext(checkCtx, namespace, pod, 64*1024, checkpointCommand, "--json")
	if err != nil {
		return nil, fmt.Errorf("execute checkpoints: %w", err)
	}
	if exitCode != 0 {
		return nil, fmt.Errorf("checkpoint runner exited with %d: %s", exitCode, strings.TrimSpace(output))
	}
	results, err := parseCheckpointReport(output, expectedIDs)
	if err != nil {
		return nil, fmt.Errorf("invalid checkpoint report: %w", err)
	}
	return results, nil
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

// recordCheckpointStatus only updates status when the visible result changes.
// This prevents controller status events from bypassing the periodic requeue.
func recordCheckpointStatus(status *breakfixv1.CommonEnvironmentStatus, results []breakfixv1.CheckpointResultStatus, checkErr error) bool {
	if status == nil {
		return false
	}
	next := &breakfixv1.CheckpointStatus{}
	if checkErr != nil {
		next.Error = truncate(checkErr.Error(), 4000)
		if status.Checkpoints != nil {
			next.Results = append([]breakfixv1.CheckpointResultStatus{}, status.Checkpoints.Results...)
		}
	} else {
		previousCount := 0
		if status.Checkpoints != nil {
			previousCount = len(status.Checkpoints.Results)
		}
		firstPassedAt := make(map[string]*metav1.Time, previousCount)
		if status.Checkpoints != nil {
			for _, result := range status.Checkpoints.Results {
				if result.FirstPassedAt != nil {
					firstPassedAt[result.ID] = result.FirstPassedAt
				}
			}
		}
		next.Results = make([]breakfixv1.CheckpointResultStatus, len(results))
		for index, result := range results {
			if recorded, ok := firstPassedAt[result.ID]; ok {
				result.FirstPassedAt = recorded
			} else if result.Passed {
				now := metav1.Now()
				result.FirstPassedAt = &now
			}
			next.Results[index] = result
		}
	}
	current := status.Checkpoints
	if current != nil && current.Error == next.Error && slices.Equal(current.Results, next.Results) {
		return false
	}
	now := metav1.Now()
	next.CheckedAt = &now
	status.Checkpoints = next
	return true
}
