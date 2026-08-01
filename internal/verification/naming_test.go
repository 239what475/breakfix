package verification

import (
	"strings"
	"testing"
)

func TestNamesAreDeterministicAndBounded(t *testing.T) {
	workflowID := strings.Repeat("generation-workflow-", 8)
	name := EnvironmentName(workflowID)
	if len(name) > 63 {
		t.Fatalf("name length = %d, want <= 63: %q", len(name), name)
	}
	if EnvironmentName(workflowID) != name {
		t.Fatal("names must be deterministic")
	}
}
