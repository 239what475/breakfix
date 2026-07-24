package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	breakfixv1 "github.com/breakfix/breakfix/apis/breakfix/v1"
)

// CompletionRecorder persists user learning progress outside the lifecycle of
// an Environment CRD. It is injected so the controller does not depend on the
// Gateway's SQLite implementation.
type CompletionRecorder interface {
	RecordChallengeCompletion(context.Context, string, string, string, time.Time) error
}

func recordEnvironmentCompletion(ctx context.Context, recorder CompletionRecorder, env commonEnvironmentObject, completedAt time.Time) error {
	if recorder == nil {
		return fmt.Errorf("completion recorder is not configured")
	}
	spec := env.CommonSpec()
	if strings.TrimSpace(spec.UserRef) == "" || strings.TrimSpace(spec.ChallengeRef) == "" {
		return fmt.Errorf("environment %q is missing completion identity", env.GetName())
	}
	if strings.TrimSpace(string(env.GetUID())) == "" {
		return fmt.Errorf("environment %q is missing uid", env.GetName())
	}
	if completedAt.IsZero() {
		return fmt.Errorf("environment %q completion time is required", env.GetName())
	}
	return recorder.RecordChallengeCompletion(ctx, spec.UserRef, spec.ChallengeRef, string(env.GetUID()), completedAt)
}

func completionTime(status *breakfixv1.CommonEnvironmentStatus) time.Time {
	if status != nil && status.CompletedAt != nil {
		return status.CompletedAt.UTC()
	}
	return time.Now().UTC()
}
