package verification

import (
	"strings"
	"testing"
)

func TestNamesAreDeterministicAndBounded(t *testing.T) {
	taskID := strings.Repeat("verify-task-", 8)
	for _, name := range []string{BuildJobName(taskID), PublisherJobName(taskID), JobName(taskID), EnvironmentName(taskID)} {
		if len(name) > 63 {
			t.Fatalf("name length = %d, want <= 63: %q", len(name), name)
		}
	}
	if BuildJobName(taskID) != BuildJobName(taskID) || PublisherJobName(taskID) != PublisherJobName(taskID) || JobName(taskID) != JobName(taskID) || EnvironmentName(taskID) != EnvironmentName(taskID) {
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

func TestPublishedImageName(t *testing.T) {
	if got, want := PublishedImageName("registry.example/team", "chal-123"), "registry.example/team/challenge-chal-123:latest"; got != want {
		t.Fatalf("PublishedImageName() = %q, want %q", got, want)
	}
	if got, want := PublishedImageName("", "chal-123"), "challenge-chal-123:latest"; got != want {
		t.Fatalf("PublishedImageName() = %q, want %q", got, want)
	}
}
