package incus

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNodeRuntimeInitReadsScenarioBundleDir(t *testing.T) {
	projectRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}

	scriptPath := filepath.Join(projectRoot, "build", "images", "node-systemd-base", "runtime-init.sh")
	script, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatal(err)
	}

	want := "bundle=" + scenarioBundleDir
	if !strings.Contains(string(script), want+"\n") {
		t.Fatalf("%s must contain %q", scriptPath, want)
	}
	legacyBundlePath := "/opt/breakfix/" + "chal" + "lenge"
	if strings.Contains(string(script), legacyBundlePath) {
		t.Fatalf("%s still refers to the legacy bundle directory", scriptPath)
	}
}
