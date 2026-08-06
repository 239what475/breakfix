package publication

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestDiagnosticKeepsTypedCategoryAndStableRetryTime(t *testing.T) {
	attemptedAt := time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC)
	err := Transient(errors.New("provider\nwas temporarily unavailable"))
	diagnostic, err := NewDiagnostic(err, attemptedAt)
	if err != nil {
		t.Fatalf("create diagnostic: %v", err)
	}
	if diagnostic.Category != CategoryTransient || diagnostic.LastError != "provider was temporarily unavailable" {
		t.Fatalf("diagnostic = %#v", diagnostic)
	}
	if diagnostic.NextRetryAt == nil || !diagnostic.NextRetryAt.Equal(RetryAt(attemptedAt)) {
		t.Fatalf("retry time = %v, want %v", diagnostic.NextRetryAt, RetryAt(attemptedAt))
	}
}

func TestSanitizeErrorBoundsDurableMessage(t *testing.T) {
	message := strings.Repeat("x", maxDiagnosticMessage+100)
	sanitized := SanitizeError(errors.New("\x00" + message))
	if len([]rune(sanitized)) != maxDiagnosticMessage {
		t.Fatalf("sanitized length = %d, want %d", len([]rune(sanitized)), maxDiagnosticMessage)
	}
	if strings.ContainsAny(sanitized, "\x00\n\r\t") {
		t.Fatalf("sanitized message contains control characters: %q", sanitized)
	}
}
