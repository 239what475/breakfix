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

func TestValidateChallengeSemanticsRejectsWrongKubectlColumnOrderPattern(t *testing.T) {
	dir := t.TempDir()
	writeGeneratorTestFile(t, filepath.Join(dir, "challenge.yaml"), `type: script
runtime: vcluster
title: Fix Deployment
difficulty: easy
tags:
  - kubernetes
description: |
  demo
`)
	writeGeneratorTestFile(t, filepath.Join(dir, "verify.sh"), `#!/bin/bash
kubectl get pods -n default -l app=web --no-headers
BAD_PODS=$(echo "${ACTIVE_PODS}" | grep -vE '(Running\s+1/1)' || echo "")
`)

	var g Generator
	err := g.validateChallengeSemantics(dir)
	if err == nil {
		t.Fatal("expected validateChallengeSemantics() to fail")
	}
	if !strings.Contains(err.Error(), "READY/STATUS 列顺序判断错误") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateChallengeSemanticsAcceptsVClusterVerifyWithoutBadPattern(t *testing.T) {
	dir := t.TempDir()
	writeGeneratorTestFile(t, filepath.Join(dir, "challenge.yaml"), `type: script
runtime: vcluster
title: Fix Deployment
difficulty: easy
tags:
  - kubernetes
description: |
  demo
`)
	writeGeneratorTestFile(t, filepath.Join(dir, "verify.sh"), `#!/bin/bash
READY=$(kubectl get deployment web -n default -o jsonpath='{.status.readyReplicas}')
REPLICAS=$(kubectl get deployment web -n default -o jsonpath='{.spec.replicas}')
[ -n "$READY" ] && [ "$READY" = "$REPLICAS" ] && [ "$READY" -gt 0 ]
`)

	var g Generator
	if err := g.validateChallengeSemantics(dir); err != nil {
		t.Fatalf("validateChallengeSemantics() error = %v", err)
	}
}

func TestValidateChallengeSemanticsRejectsEphemeralProbePodsForVCluster(t *testing.T) {
	dir := t.TempDir()
	writeGeneratorTestFile(t, filepath.Join(dir, "challenge.yaml"), `type: script
runtime: vcluster
title: Fix Deployment
difficulty: easy
tags:
  - kubernetes
description: |
  demo
`)
	writeGeneratorTestFile(t, filepath.Join(dir, "verify.sh"), `#!/bin/bash
kubectl run verify-http-test --rm -i --restart=Never --image=busybox:1.36 -- wget -qO- http://web
`)

	var g Generator
	err := g.validateChallengeSemantics(dir)
	if err == nil {
		t.Fatal("expected validateChallengeSemantics() to fail")
	}
	if !strings.Contains(err.Error(), "不应依赖 `kubectl run`") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateChallengeSemanticsRejectsNaivePodHealthLoopForVCluster(t *testing.T) {
	dir := t.TempDir()
	writeGeneratorTestFile(t, filepath.Join(dir, "challenge.yaml"), `type: script
runtime: vcluster
title: Fix Deployment
difficulty: easy
tags:
  - kubernetes
description: |
  demo
`)
	writeGeneratorTestFile(t, filepath.Join(dir, "verify.sh"), `#!/bin/bash
POD_LINES=$(kubectl get pods -n default -l app=web --no-headers 2>/dev/null)
while IFS= read -r line; do
    STATUS=$(echo "$line" | awk '{print $3}')
    READY_COL=$(echo "$line" | awk '{print $2}')
    if [ "$STATUS" != "Running" ] || [ "$READY_COL" != "1/1" ]; then
        echo "FAIL: Pod not healthy — $line"
        exit 1
    fi
done <<< "$POD_LINES"
`)

	var g Generator
	err := g.validateChallengeSemantics(dir)
	if err == nil {
		t.Fatal("expected validateChallengeSemantics() to fail")
	}
	if !strings.Contains(err.Error(), "不应通过遍历标签下的所有 Pod 并硬判") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateChallengeSemanticsRejectsNaivePodGrepFilterForVCluster(t *testing.T) {
	dir := t.TempDir()
	writeGeneratorTestFile(t, filepath.Join(dir, "challenge.yaml"), `type: script
runtime: vcluster
title: Fix Deployment
difficulty: easy
tags:
  - kubernetes
description: |
  demo
`)
	writeGeneratorTestFile(t, filepath.Join(dir, "verify.sh"), `#!/bin/bash
POD_OUTPUT=$(kubectl get pods -n default -l app=web --no-headers)
NOT_READY=$(echo "$POD_OUTPUT" | grep -v -E '1/1\s+Running' | wc -l || true)
if [ "$NOT_READY" -ne 0 ]; then
  echo "FAIL"
  exit 1
fi
`)

	var g Generator
	err := g.validateChallengeSemantics(dir)
	if err == nil {
		t.Fatal("expected validateChallengeSemantics() to fail")
	}
	if !strings.Contains(err.Error(), "不应通过 `kubectl get pods ... | grep -v") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func writeGeneratorTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
