package verification

import (
	"strings"
	"testing"
)

func TestNamesAreDeterministicAndBounded(t *testing.T) {
	taskID := strings.Repeat("verify-task-", 8)
	for _, name := range []string{JobName(taskID), EnvironmentName(taskID)} {
		if len(name) > 63 {
			t.Fatalf("name length = %d, want <= 63: %q", len(name), name)
		}
	}
	if JobName(taskID) != JobName(taskID) || EnvironmentName(taskID) != EnvironmentName(taskID) {
		t.Fatal("names must be deterministic")
	}
}

func TestImageName(t *testing.T) {
	if got, want := ImageName("registry.example/team", "vt-123"), "registry.example/team/verify-vt-123:latest"; got != want {
		t.Fatalf("ImageName() = %q, want %q", got, want)
	}
	if got, want := ImageName("", "vt-123"), "verify-vt-123:latest"; got != want {
		t.Fatalf("ImageName() = %q, want %q", got, want)
	}
}
