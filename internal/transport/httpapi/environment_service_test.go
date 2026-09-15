package httpapi

import (
	"strings"
	"testing"

	runtimev2 "github.com/breakfix/breakfix/api/v2"
)

func TestEnvironmentUnavailableErrorHidesInfrastructureDiagnostics(t *testing.T) {
	err := environmentUnavailableError(&activeEnvironment{Failure: &runtimev2.EnvironmentFailure{
		Class:   runtimev2.FailureInfrastructure,
		Reason:  "ProviderUnavailable",
		Message: "dial https://incus.internal.example:8443: connection refused",
	}})
	if err == nil {
		t.Fatal("expected an unavailable environment error")
	}
	if got, want := err.Error(), "learning environment is temporarily unavailable; please try again"; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
	if strings.Contains(err.Error(), "incus") || strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("browser error leaked provider diagnostics: %q", err)
	}
}
