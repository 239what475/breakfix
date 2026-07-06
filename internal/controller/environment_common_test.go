package controller

import (
	"testing"
	"time"

	breakfixv1 "github.com/breakfix/breakfix/apis/breakfix/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestSetEnvironmentReadyAssignsLeaseWhenMissing(t *testing.T) {
	var status breakfixv1.CommonEnvironmentStatus

	setEnvironmentReady(&status, "ready")

	if status.Phase != breakfixv1.EnvironmentReady {
		t.Fatalf("expected Ready, got %s", status.Phase)
	}
	if status.ExpiresAt == nil {
		t.Fatal("expected expiresAt to be assigned")
	}
	if status.StartedAt == nil {
		t.Fatal("expected startedAt to be assigned")
	}
	if !status.ExpiresAt.After(status.StartedAt.Time) {
		t.Fatalf("expected expiresAt after startedAt, got startedAt=%v expiresAt=%v", status.StartedAt, status.ExpiresAt)
	}
}

func TestSetEnvironmentReadyPreservesActiveLease(t *testing.T) {
	expires := time.Now().Add(3 * time.Minute)
	status := breakfixv1.CommonEnvironmentStatus{
		ExpiresAt: ptrMetaTime(expires),
	}

	setEnvironmentReady(&status, "ready")

	if status.ExpiresAt == nil {
		t.Fatal("expected expiresAt to remain set")
	}
	if got := status.ExpiresAt.Time; !got.Equal(expires) {
		t.Fatalf("expected existing expiresAt %v, got %v", expires, got)
	}
}

func ptrMetaTime(t time.Time) *metav1.Time {
	mt := metav1.NewTime(t)
	return &mt
}
