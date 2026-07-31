package opensandbox

import "testing"

func TestProviderFileModeUsesOpenSandboxOctalNotation(t *testing.T) {
	for _, test := range []struct {
		mode int
		want int
	}{
		{mode: 0o600, want: 600},
		{mode: 0o644, want: 644},
		{mode: 0o755, want: 755},
		{mode: 0o4755, want: 755},
	} {
		if got := providerFileMode(test.mode); got != test.want {
			t.Errorf("providerFileMode(%#o) = %d, want %d", test.mode, got, test.want)
		}
	}
}

func TestWorkspacePathAcceptsEinoRelativePrefix(t *testing.T) {
	for _, value := range []string{"challenge.yaml", "./challenge.yaml", "nodes/host/checks.sh", "./nodes/host/checks.sh"} {
		if err := ValidateWorkspacePath(value); err != nil {
			t.Fatalf("ValidateWorkspacePath(%q): %v", value, err)
		}
	}
	if got, want := WorkspacePath("./challenge.yaml"), "/workspace/challenge.yaml"; got != want {
		t.Fatalf("WorkspacePath(./challenge.yaml) = %q, want %q", got, want)
	}
}

func TestWorkspacePathRejectsEscapesAndAmbiguousRelativePaths(t *testing.T) {
	for _, value := range []string{"", ".", "./", "././challenge.yaml", "../challenge.yaml", "checks/../challenge.yaml", "/etc/passwd", `checks\\bad`} {
		if err := ValidateWorkspacePath(value); err == nil {
			t.Fatalf("ValidateWorkspacePath(%q) unexpectedly succeeded", value)
		}
	}
}
