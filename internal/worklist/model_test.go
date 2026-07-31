package worklist

import (
	"testing"
	"time"
)

func TestClaimRetryDelay(t *testing.T) {
	tests := []struct {
		name     string
		base     time.Duration
		failures int
		want     time.Duration
	}{
		{name: "default base", base: 0, failures: 1, want: time.Second},
		{name: "first failure", base: time.Second, failures: 1, want: time.Second},
		{name: "second failure", base: time.Second, failures: 2, want: 2 * time.Second},
		{name: "caps after multiplication", base: 20 * time.Second, failures: 2, want: 30 * time.Second},
		{name: "caps oversized base", base: time.Hour, failures: 1, want: 30 * time.Second},
		{name: "caps repeated failures", base: time.Millisecond, failures: 100, want: 30 * time.Second},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ClaimRetryDelay(test.base, test.failures); got != test.want {
				t.Fatalf("ClaimRetryDelay(%s, %d) = %s, want %s", test.base, test.failures, got, test.want)
			}
		})
	}
}
