package generation

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateScenarioManifestRejectsPlatformFields(t *testing.T) {
	dir := t.TempDir()
	writeGeneratorTestFile(t, filepath.Join(dir, "scenario.yaml"), `id: should-disappear
image: should-disappear
runtime: node
title: Cleanup Logs
description: |
  修复日志清理流程并恢复磁盘空间。
`)

	_, err := ValidateCandidateDir(dir)
	if err == nil {
		t.Fatal("expected validateScenarioManifest() to reject platform fields")
	}
	if !strings.Contains(err.Error(), "平台托管字段") {
		t.Fatalf("expected platform-field error, got %v", err)
	}
	data, readErr := os.ReadFile(filepath.Join(dir, "scenario.yaml"))
	if readErr != nil {
		t.Fatalf("read scenario.yaml: %v", readErr)
	}
	got := string(data)
	if !strings.Contains(got, "id: should-disappear") || !strings.Contains(got, "image: should-disappear") {
		t.Fatalf("validation unexpectedly rewrote scenario.yaml:\n%s", got)
	}
}

func TestValidateScenarioManifestRejectsMissingMetadata(t *testing.T) {
	dir := t.TempDir()
	writeGeneratorTestFile(t, filepath.Join(dir, "scenario.yaml"), `runtime: node
title: Cleanup Logs
description: ""
`)

	_, err := ValidateCandidateDir(dir)
	if err == nil {
		t.Fatal("expected validateScenarioManifest() to fail")
	}
	msg := err.Error()
	if !strings.Contains(msg, "缺少 description") {
		t.Fatalf("expected description error, got %q", msg)
	}
}

func TestValidateCandidateDirRejectsDocumentationExample(t *testing.T) {
	dir := t.TempDir()
	writeGeneratorTestFile(t, filepath.Join(dir, "scenario.yaml"), `type: documentation-example
runtime: node
title: Observe a process
description: Observe one process state.
nodes:
  - name: host
    title: Host
`)
	writeGeneratorTestFile(t, filepath.Join(dir, "nodes", "host", "generate.sh"), "#!/bin/sh\n")

	_, err := ValidateCandidateDir(dir)
	if err == nil || !strings.Contains(err.Error(), "operations module accepts only operations-scenario") {
		t.Fatalf("ValidateCandidateDir error = %v", err)
	}
}

func writeGeneratorTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
