package generator

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/breakfix/breakfix/internal/authoring"
	"github.com/breakfix/breakfix/internal/challenge"
)

func TestReviewedPlanContextUsesEachPlanSectionOnce(t *testing.T) {
	plan := &authoring.Plan{
		Metadata: authoring.Metadata{
			Title:       "唯一标题",
			Description: "唯一简介",
			Difficulty:  "medium",
			Runtime:     challenge.RuntimeContainer,
		},
		Overview:    "唯一概览",
		Checkpoints: []authoring.Checkpoint{{ID: "old-logs", Title: "唯一检查点", Markdown: "唯一检查点说明", Position: 1}},
	}

	context := (&Generator{Plan: plan}).reviewedPlanContext()
	for _, value := range []string{"唯一标题", "唯一简介", "唯一概览", "唯一检查点说明"} {
		if count := strings.Count(context, value); count != 1 {
			t.Fatalf("reviewedPlanContext() contains %q %d times, want once:\n%s", value, count, context)
		}
	}
	if strings.Contains(context, "表象：") || strings.Contains(context, "故障机制：") || strings.Contains(context, "验收标准：") {
		t.Fatalf("reviewedPlanContext() retained legacy draft fields:\n%s", context)
	}
}

func TestArchiveDirPreservesNestedChallengeAssets(t *testing.T) {
	source := t.TempDir()
	writeGeneratorSemanticChallenge(t, source, "container", "#!/bin/sh\nprintf '{\"checks\":[]}'\n")

	payload, err := archiveDir(source)
	if err != nil {
		t.Fatalf("archiveDir: %v", err)
	}
	destination := t.TempDir()
	if err := challenge.ExtractTarGz(destination, bytes.NewReader(payload)); err != nil {
		t.Fatalf("ExtractTarGz: %v", err)
	}
	if _, err := challenge.ValidateSubmissionDir(destination); err != nil {
		t.Fatalf("archived challenge no longer validates: %v", err)
	}
	for _, name := range []string{"checks/checkpoints.sh", "hints/deployment-ready.md"} {
		if _, err := os.Stat(filepath.Join(destination, name)); err != nil {
			t.Fatalf("archive omitted nested asset %s: %v", name, err)
		}
	}
}

func TestValidateChallengeManifestRejectsPlatformFields(t *testing.T) {
	dir := t.TempDir()
	writeGeneratorTestFile(t, filepath.Join(dir, "challenge.yaml"), `id: should-disappear
image: should-disappear
type: script
runtime: container
title: Cleanup Logs
difficulty: medium
description: |
  修复日志清理流程并恢复磁盘空间。
`)

	var g Generator
	err := g.validateChallengeManifest(dir)
	if err == nil {
		t.Fatal("expected validateChallengeManifest() to reject platform fields")
	}
	if !strings.Contains(err.Error(), "平台托管字段") {
		t.Fatalf("expected platform-field error, got %v", err)
	}
	data, readErr := os.ReadFile(filepath.Join(dir, "challenge.yaml"))
	if readErr != nil {
		t.Fatalf("read challenge.yaml: %v", readErr)
	}
	got := string(data)
	if !strings.Contains(got, "id: should-disappear") || !strings.Contains(got, "image: should-disappear") {
		t.Fatalf("validation unexpectedly rewrote challenge.yaml:\n%s", got)
	}
}

func TestValidateChallengeManifestRejectsMissingMetadata(t *testing.T) {
	dir := t.TempDir()
	writeGeneratorTestFile(t, filepath.Join(dir, "challenge.yaml"), `type: script
title: Cleanup Logs
difficulty: medium
description: ""
`)

	var g Generator
	err := g.validateChallengeManifest(dir)
	if err == nil {
		t.Fatal("expected validateChallengeManifest() to fail")
	}
	msg := err.Error()
	if !strings.Contains(msg, "缺少 description") {
		t.Fatalf("expected description error, got %q", msg)
	}
}

func TestValidateChallengeSemanticsRejectsWrongKubectlColumnOrderPattern(t *testing.T) {
	dir := t.TempDir()
	writeGeneratorSemanticChallenge(t, dir, "vcluster", `#!/bin/bash
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
	writeGeneratorSemanticChallenge(t, dir, "vcluster", `#!/bin/bash
READY=$(kubectl get deployment web -n default -o jsonpath='{.status.readyReplicas}')
REPLICAS=$(kubectl get deployment web -n default -o jsonpath='{.spec.replicas}')
[ -n "$READY" ] && [ "$READY" = "$REPLICAS" ] && [ "$READY" -gt 0 ]
`)

	var g Generator
	if err := g.validateChallengeSemantics(dir); err != nil {
		t.Fatalf("validateChallengeSemantics() error = %v", err)
	}
}

func TestValidateChallengeSemanticsRejectsBuildTimeNetworkInstall(t *testing.T) {
	dir := t.TempDir()
	writeGeneratorSemanticChallenge(t, dir, "container", "#!/bin/sh\nprintf '{\"checks\":[]}'\n")
	writeGeneratorTestFile(t, filepath.Join(dir, "Dockerfile"), "FROM breakfix-base:latest\nRUN apt-get update && apt-get install -y curl\n")

	var g Generator
	err := g.validateChallengeSemantics(dir)
	if err == nil {
		t.Fatal("expected validateChallengeSemantics() to reject build-time package installation")
	}
	if !strings.Contains(err.Error(), "构建期使用") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateChallengeSemanticsRejectsEphemeralProbePodsForVCluster(t *testing.T) {
	dir := t.TempDir()
	writeGeneratorSemanticChallenge(t, dir, "vcluster", `#!/bin/bash
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
	writeGeneratorSemanticChallenge(t, dir, "vcluster", `#!/bin/bash
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
	writeGeneratorSemanticChallenge(t, dir, "vcluster", `#!/bin/bash
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

func TestJudgeResponsePassedRequiresExactPass(t *testing.T) {
	tests := []struct {
		name     string
		response string
		want     bool
	}{
		{name: "plain pass", response: "PASS", want: true},
		{name: "trailing newline is invalid", response: "PASS\n", want: false},
		{name: "plain failure", response: "FAIL: missing answer", want: false},
		{name: "report with final pass is invalid", response: "检查完成。\n**Final Verdict: PASS**", want: false},
		{name: "Chinese Markdown pass is invalid", response: "## 审查结果：PASS", want: false},
		{name: "JSON pass is invalid", response: `{"pass": true}`, want: false},
		{name: "no explicit verdict", response: "All files appear correct.", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := judgeResponsePassed(tt.response); got != tt.want {
				t.Fatalf("judgeResponsePassed(%q) = %t, want %t", tt.response, got, tt.want)
			}
		})
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

func writeGeneratorSemanticChallenge(t *testing.T, dir, runtime, checkpointScript string) {
	t.Helper()
	writeGeneratorTestFile(t, filepath.Join(dir, "challenge.yaml"), "type: script\nruntime: "+runtime+"\ntitle: Fix Deployment\ndifficulty: easy\ndescription: demo\ncheckpoints:\n  - id: deployment-ready\n    title: Deployment ready\n    description: The deployment is ready\n    hint: hints/deployment-ready.md\n")
	writeGeneratorTestFile(t, filepath.Join(dir, "Dockerfile"), "FROM breakfix-k8s-base:latest\n")
	writeGeneratorTestFile(t, filepath.Join(dir, "generate.sh"), "#!/bin/sh\n")
	writeGeneratorTestFile(t, filepath.Join(dir, "problem.md"), "problem\n")
	writeGeneratorTestFile(t, filepath.Join(dir, "solution.md"), "solution\n")
	writeGeneratorTestFile(t, filepath.Join(dir, "hints", "deployment-ready.md"), "hint\n")
	writeGeneratorTestFile(t, filepath.Join(dir, "checks", "checkpoints.sh"), checkpointScript)
	writeGeneratorTestFile(t, filepath.Join(dir, "answer.sh"), "#!/bin/sh\n")
}
