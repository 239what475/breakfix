package taxonomy

import (
	"errors"
	"testing"
	"time"
)

func TestReconcileDelayBacksOffAfterFailure(t *testing.T) {
	if got := reconcileDelay(true, errors.New("temporary taxonomy failure")); got != failedReconcileDelay {
		t.Fatalf("failed reconcile delay = %s, want %s", got, failedReconcileDelay)
	}
	if got := reconcileDelay(true, nil); got != 10*time.Millisecond {
		t.Fatalf("successful reconcile delay = %s, want 10ms", got)
	}
	if got := reconcileDelay(false, nil); got != time.Second {
		t.Fatalf("idle reconcile delay = %s, want 1s", got)
	}
}
