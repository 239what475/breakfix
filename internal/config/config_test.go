package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestExampleConfigLoads(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate config test source")
	}
	examplePath := filepath.Join(filepath.Dir(source), "..", "..", "config", "breakfix.example.yaml")
	if _, err := Load(examplePath); err != nil {
		t.Fatalf("load example config: %v", err)
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "breakfix.yaml")
	if err := os.WriteFile(path, []byte("port: 9090\nunknown_setting: true\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() succeeded with an unknown field")
	}
	if !strings.Contains(err.Error(), "unknown_setting") {
		t.Fatalf("Load() error = %q, want unknown field name", err)
	}
}
