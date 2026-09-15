package incus

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNodeRuntimeInitReadsRunnableBundleDir(t *testing.T) {
	projectRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}

	scriptPath := filepath.Join(projectRoot, "build", "images", "node-systemd-base", "runtime-init.sh")
	script, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatal(err)
	}

	want := "bundle=" + runnableBundleDir
	if !strings.Contains(string(script), want+"\n") {
		t.Fatalf("%s must contain %q", scriptPath, want)
	}
	if strings.Contains(string(script), "/opt/breakfix/scenario") {
		t.Fatalf("%s still refers to the retired scenario bundle directory", scriptPath)
	}
}
