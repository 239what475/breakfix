package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	breakfixv1 "github.com/breakfix/breakfix/internal/k8s/apis/breakfix/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

type completionRecord struct {
	userID         string
	challengeID    string
	environmentUID string
	completedAt    time.Time
}

type recordingCompletionRecorder struct {
	records []completionRecord
	err     error
}

type attemptRecord struct {
	userID         string
	challengeID    string
	environmentUID string
	runtime        string
	readyAt        time.Time
	outcome        string
	endedAt        time.Time
}

type recordingAttemptRecorder struct {
	records []attemptRecord
	err     error
}

func (r *recordingAttemptRecorder) RecordChallengeAttempt(_ context.Context, userID, challengeID, environmentUID, runtime string, readyAt time.Time) error {
	if r.err != nil {
		return r.err
	}
	r.records = append(r.records, attemptRecord{userID: userID, challengeID: challengeID, environmentUID: environmentUID, runtime: runtime, readyAt: readyAt})
	return nil
}

func (r *recordingAttemptRecorder) FinishChallengeAttempt(_ context.Context, environmentUID, outcome string, endedAt time.Time) error {
	if r.err != nil {
		return r.err
	}
	r.records = append(r.records, attemptRecord{environmentUID: environmentUID, outcome: outcome, endedAt: endedAt})
	return nil
}

func TestReadyEnvironmentRecordsAttemptBeforeStatusPersistence(t *testing.T) {
	env := &breakfixv1.ContainerEnvironment{
		ObjectMeta: metav1.ObjectMeta{Name: "container-env", UID: types.UID("container-uid")},
		Spec:       breakfixv1.CommonEnvironmentSpec{UserRef: "user-a", ChallengeRef: "challenge-a"},
	}
	recorder := &recordingAttemptRecorder{}
	if err := setEnvironmentReadyAndRecordAttempt(context.Background(), recorder, env, "container", "WorkspaceReady", "environment ready"); err != nil {
		t.Fatal(err)
	}
	if env.Status.Phase != breakfixv1.EnvironmentReady || env.Status.ReadyAt == nil {
		t.Fatalf("environment did not transition to ready: %#v", env.Status)
	}
	if len(recorder.records) != 1 {
		t.Fatalf("attempt records = %#v", recorder.records)
	}
	record := recorder.records[0]
	if record.userID != "user-a" || record.challengeID != "challenge-a" || record.environmentUID != "container-uid" || record.runtime != "container" || !record.readyAt.Equal(env.Status.ReadyAt.Time) {
		t.Fatalf("ready attempt = %#v", record)
	}
}

func TestReadyEnvironmentDoesNotPermitStatusPersistenceWhenAttemptFails(t *testing.T) {
	env := &breakfixv1.ContainerEnvironment{
		ObjectMeta: metav1.ObjectMeta{Name: "container-env", UID: types.UID("container-uid")},
		Spec:       breakfixv1.CommonEnvironmentSpec{UserRef: "user-a", ChallengeRef: "challenge-a"},
	}
	err := setEnvironmentReadyAndRecordAttempt(context.Background(), &recordingAttemptRecorder{err: errors.New("database unavailable")}, env, "container", "WorkspaceReady", "environment ready")
	if err == nil {
		t.Fatal("expected attempt persistence failure")
	}
	// waitForPod returns before calling Status().Update when this helper fails.
	if env.Status.Phase != breakfixv1.EnvironmentReady {
		t.Fatalf("unexpected in-memory transition: %#v", env.Status)
	}
}

func TestFinalCleanupFinishesReadyAttemptAsExpired(t *testing.T) {
	readyAt := metav1.NewTime(time.Date(2026, time.July, 24, 10, 30, 0, 0, time.UTC))
	env := &breakfixv1.ContainerEnvironment{
		ObjectMeta: metav1.ObjectMeta{Name: "container-env", UID: types.UID("container-uid")},
		Status:     breakfixv1.CommonEnvironmentStatus{ReadyAt: &readyAt},
	}
	recorder := &recordingAttemptRecorder{}
	endedAt := readyAt.Add(time.Minute)
	if err := finishEnvironmentAttempt(context.Background(), recorder, env, "expired", endedAt); err != nil {
		t.Fatal(err)
	}
	if len(recorder.records) != 1 || recorder.records[0].environmentUID != "container-uid" || recorder.records[0].outcome != "expired" || !recorder.records[0].endedAt.Equal(endedAt) {
		t.Fatalf("final cleanup records = %#v", recorder.records)
	}
}

func (r *recordingCompletionRecorder) RecordChallengeCompletion(_ context.Context, userID, challengeID, environmentUID string, completedAt time.Time) error {
	if r.err != nil {
		return r.err
	}
	r.records = append(r.records, completionRecord{
		userID: userID, challengeID: challengeID, environmentUID: environmentUID, completedAt: completedAt,
	})
	return nil
}

func TestCompletedContainerEnvironmentRecordsProgressBeforeCleanup(t *testing.T) {
	completedAt := metav1.NewTime(time.Date(2026, time.July, 24, 10, 0, 0, 0, time.UTC))
	env := &breakfixv1.ContainerEnvironment{
		ObjectMeta: metav1.ObjectMeta{Name: "container-env", UID: types.UID("container-uid")},
		Spec:       breakfixv1.CommonEnvironmentSpec{UserRef: "user-a", ChallengeRef: "challenge-a"},
		Status: breakfixv1.CommonEnvironmentStatus{
			Phase:       breakfixv1.EnvironmentCompleted,
			CompletedAt: &completedAt,
			ExpiresAt:   &metav1.Time{Time: time.Now().Add(time.Minute)},
		},
	}
	recorder := &recordingCompletionRecorder{}
	runtime := containerEnvironmentRuntime{r: &ContainerEnvironmentReconciler{CompletionRecorder: recorder}}

	if _, err := runtime.handleDraining(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	assertRecordedCompletion(t, recorder, "user-a", "challenge-a", "container-uid", completedAt.Time)
}

func TestCompletedVClusterEnvironmentRecordsProgressBeforeCleanup(t *testing.T) {
	completedAt := metav1.NewTime(time.Date(2026, time.July, 24, 10, 5, 0, 0, time.UTC))
	env := &breakfixv1.VClusterEnvironment{
		ObjectMeta: metav1.ObjectMeta{Name: "vcluster-env", UID: types.UID("vcluster-uid")},
		Spec: breakfixv1.VClusterEnvironmentSpec{CommonEnvironmentSpec: breakfixv1.CommonEnvironmentSpec{
			UserRef: "user-a", ChallengeRef: "challenge-a",
		}},
		Status: breakfixv1.VClusterEnvironmentStatus{CommonEnvironmentStatus: breakfixv1.CommonEnvironmentStatus{
			Phase:       breakfixv1.EnvironmentCompleted,
			CompletedAt: &completedAt,
			ExpiresAt:   &metav1.Time{Time: time.Now().Add(time.Minute)},
		}},
	}
	recorder := &recordingCompletionRecorder{}
	runtime := vclusterEnvironmentRuntime{r: &VClusterEnvironmentReconciler{CompletionRecorder: recorder}}

	if _, err := runtime.handleDraining(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	assertRecordedCompletion(t, recorder, "user-a", "challenge-a", "vcluster-uid", completedAt.Time)
}

func TestCompletedEnvironmentDoesNotCleanUpWhenProgressPersistenceFails(t *testing.T) {
	env := &breakfixv1.ContainerEnvironment{
		ObjectMeta: metav1.ObjectMeta{Name: "container-env", UID: types.UID("container-uid")},
		Spec:       breakfixv1.CommonEnvironmentSpec{UserRef: "user-a", ChallengeRef: "challenge-a"},
		Status: breakfixv1.CommonEnvironmentStatus{
			Phase:     breakfixv1.EnvironmentCompleted,
			ExpiresAt: &metav1.Time{Time: time.Now().Add(time.Minute)},
		},
	}
	runtime := containerEnvironmentRuntime{r: &ContainerEnvironmentReconciler{
		CompletionRecorder: &recordingCompletionRecorder{err: errors.New("sqlite unavailable")},
	}}

	if _, err := runtime.handleDraining(context.Background(), env); err == nil {
		t.Fatal("expected persistence failure")
	}
	if env.Status.Phase != breakfixv1.EnvironmentCompleted {
		t.Fatalf("environment was mutated after persistence failure: %s", env.Status.Phase)
	}
}

func assertRecordedCompletion(t *testing.T, recorder *recordingCompletionRecorder, userID, challengeID, environmentUID string, completedAt time.Time) {
	t.Helper()
	if len(recorder.records) != 1 {
		t.Fatalf("completion records = %#v", recorder.records)
	}
	got := recorder.records[0]
	if got.userID != userID || got.challengeID != challengeID || got.environmentUID != environmentUID || !got.completedAt.Equal(completedAt) {
		t.Fatalf("completion record = %#v", got)
	}
}
