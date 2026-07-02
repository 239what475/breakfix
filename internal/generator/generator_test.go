package generator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateChallengeManifestStripsPlatformFields(t *testing.T) {
	dir := t.TempDir()
	writeGeneratorTestFile(t, filepath.Join(dir, "challenge.yaml"), `id: should-disappear
image: should-disappear
type: script
title: Cleanup Logs
difficulty: medium
tags:
  - linux
  - logs
description: |
  修复日志清理流程并恢复磁盘空间。
`)

	var g Generator
	if err := g.validateChallengeManifest(dir); err != nil {
		t.Fatalf("validateChallengeManifest() error = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "challenge.yaml"))
	if err != nil {
		t.Fatalf("read challenge.yaml: %v", err)
	}
	got := string(data)
	if strings.Contains(got, "id:") {
		t.Fatalf("expected id to be stripped, got:\n%s", got)
	}
	if strings.Contains(got, "image:") {
		t.Fatalf("expected image to be stripped, got:\n%s", got)
	}
}

func TestValidateChallengeManifestRejectsMissingMetadata(t *testing.T) {
	dir := t.TempDir()
	writeGeneratorTestFile(t, filepath.Join(dir, "challenge.yaml"), `type: script
title: Cleanup Logs
difficulty: medium
tags: []
description: ""
`)

	var g Generator
	err := g.validateChallengeManifest(dir)
	if err == nil {
		t.Fatal("expected validateChallengeManifest() to fail")
	}
	msg := err.Error()
	if !strings.Contains(msg, "缺少非空 tags") {
		t.Fatalf("expected tags error, got %q", msg)
	}
	if !strings.Contains(msg, "缺少 description") {
		t.Fatalf("expected description error, got %q", msg)
	}
}

func writeGeneratorTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
