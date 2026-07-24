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

// AttemptRecorder stores the durable lifecycle of a Ready environment. It is
// deliberately separate from CompletionRecorder so the controller keeps its
// dependency at the learning-record boundary rather than on a concrete DB.
type AttemptRecorder interface {
	RecordChallengeAttempt(context.Context, string, string, string, string, time.Time) error
	FinishChallengeAttempt(context.Context, string, string, time.Time) error
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

func recordEnvironmentAttempt(ctx context.Context, recorder AttemptRecorder, env commonEnvironmentObject, runtime string, readyAt time.Time) error {
	if recorder == nil {
		return fmt.Errorf("attempt recorder is not configured")
	}
	spec := env.CommonSpec()
	if strings.TrimSpace(spec.UserRef) == "" || strings.TrimSpace(spec.ChallengeRef) == "" {
		return fmt.Errorf("environment %q is missing attempt identity", env.GetName())
	}
	if strings.TrimSpace(string(env.GetUID())) == "" {
		return fmt.Errorf("environment %q is missing uid", env.GetName())
	}
	if strings.TrimSpace(runtime) == "" {
		return fmt.Errorf("environment %q is missing runtime", env.GetName())
	}
	if readyAt.IsZero() {
		return fmt.Errorf("environment %q ready time is required", env.GetName())
	}
	return recorder.RecordChallengeAttempt(ctx, spec.UserRef, spec.ChallengeRef, string(env.GetUID()), runtime, readyAt)
}

// setEnvironmentReadyAndRecordAttempt keeps the Ready transition atomic from
// the controller's point of view: callers persist status only after this
// returns successfully, so a missing durable attempt always requeues.
func setEnvironmentReadyAndRecordAttempt(ctx context.Context, recorder AttemptRecorder, env commonEnvironmentObject, runtime, reason, message string) error {
	spec := env.CommonSpec()
	status := env.CommonStatus()
	setEnvironmentReady(spec, status, reason, message)
	return recordEnvironmentAttempt(ctx, recorder, env, runtime, status.ReadyAt.Time)
}

func finishEnvironmentAttempt(ctx context.Context, recorder AttemptRecorder, env commonEnvironmentObject, outcome string, endedAt time.Time) error {
	status := env.CommonStatus()
	// An environment that never became usable is not an attempt. This also
	// permits cleanup of failed provisioning without creating learning facts.
	if status == nil || status.ReadyAt == nil || status.ReadyAt.IsZero() {
		return nil
	}
	if recorder == nil {
		return fmt.Errorf("attempt recorder is not configured")
	}
	if strings.TrimSpace(string(env.GetUID())) == "" {
		return fmt.Errorf("environment %q is missing uid", env.GetName())
	}
	if endedAt.IsZero() {
		return fmt.Errorf("environment %q attempt end time is required", env.GetName())
	}
	return recorder.FinishChallengeAttempt(ctx, string(env.GetUID()), outcome, endedAt)
}
