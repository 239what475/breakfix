package controller

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	breakfixv1 "github.com/breakfix/breakfix/internal/k8s/apis/breakfix/v1"
	"github.com/breakfix/breakfix/internal/challenge"
	"github.com/breakfix/breakfix/internal/k8s"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const checkpointInterval = 4 * time.Second

// runCheckpointEvaluation executes the single challenge verification contract.
// A non-zero result is a checker error, not an unfinished user task.
func runCheckpointEvaluation(ctx context.Context, client *k8s.Client, challengesDir, challengeID, namespace, pod string) (*challenge.CheckReport, error) {
	entry, err := challenge.Get(challengesDir, challengeID)
	if err != nil {
		return nil, fmt.Errorf("load challenge checkpoints: %w", err)
	}

	checkCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	exitCode, output, err := client.ExecInPodContext(checkCtx, namespace, pod, 64*1024, challenge.CheckpointCommand, "--json")
	if err != nil {
		return nil, fmt.Errorf("execute checkpoints: %w", err)
	}
	if exitCode != 0 {
		return nil, fmt.Errorf("checkpoint runner exited with %d: %s", exitCode, strings.TrimSpace(output))
	}
	report, err := challenge.ParseCheckReport(output, entry.Checkpoints)
	if err != nil {
		return nil, fmt.Errorf("invalid checkpoint report: %w", err)
	}
	return report, nil
}

// recordCheckpointStatus only updates status when the visible result changes.
// This prevents controller status events from bypassing the periodic requeue.
func recordCheckpointStatus(status *breakfixv1.CommonEnvironmentStatus, report *challenge.CheckReport, checkErr error) bool {
	if status == nil {
		return false
	}
	next := &breakfixv1.CheckpointStatus{}
	if checkErr != nil {
		next.Error = truncate(checkErr.Error(), 4000)
	} else if report != nil {
		next.Results = make([]breakfixv1.CheckpointResultStatus, 0, len(report.Checks))
		for _, check := range report.Checks {
			next.Results = append(next.Results, breakfixv1.CheckpointResultStatus{
				ID:      check.ID,
				Passed:  check.Passed,
				Summary: check.Summary,
				Details: check.Details,
			})
		}
	}
	current := status.Checkpoints
	if current != nil && current.Error == next.Error && slices.EqualFunc(current.Results, next.Results, func(a, b breakfixv1.CheckpointResultStatus) bool {
		return a == b
	}) {
		return false
	}
	now := metav1.Now()
	next.CheckedAt = &now
	status.Checkpoints = next
	return true
}
