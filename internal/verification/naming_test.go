package verification

import (
	"strings"
	"testing"
)

func TestNamesAreDeterministicAndBounded(t *testing.T) {
	workItemID := strings.Repeat("work-item-", 8)
	name := EnvironmentName(workItemID)
	if len(name) > 63 {
		t.Fatalf("name length = %d, want <= 63: %q", len(name), name)
	}
	if EnvironmentName(workItemID) != name {
		t.Fatal("names must be deterministic")
	}
}
