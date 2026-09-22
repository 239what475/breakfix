package runnable

import (
	"testing"
	"time"
)

// The shared curve doubles from the base and caps at five minutes from the
// seventh attempt on; both queue families ride it, so the sequence itself is
// the contract.
func TestRetryBackoffDoublesThenCapsConstant(t *testing.T) {
	base := 5 * time.Second
	want := []time.Duration{
		5 * time.Second, 10 * time.Second, 20 * time.Second, 40 * time.Second, 80 * time.Second, 160 * time.Second,
		5 * time.Minute, 5 * time.Minute, 5 * time.Minute,
	}
	for attempt, delay := range want {
		if got := RetryBackoff(base, int64(attempt+1)); got != delay {
			t.Fatalf("RetryBackoff(attempt %d) = %s, want %s", attempt+1, got, delay)
		}
	}
}

func TestRetryBackoffNormalizesDegenerateInput(t *testing.T) {
	if got := RetryBackoff(0, 0); got != 5*time.Second {
		t.Fatalf("RetryBackoff(0, 0) = %s, want the 5s default base", got)
	}
	if got := RetryBackoff(time.Minute, -3); got != time.Minute {
		t.Fatalf("RetryBackoff(1m, -3) = %s, want the first-attempt delay", got)
	}
}
