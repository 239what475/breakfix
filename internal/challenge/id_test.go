package challenge

import (
	"strings"
	"testing"
)

func TestDeriveIDFromASCIIText(t *testing.T) {
	got := DeriveID("Fix Deployment Image Tag")
	if got != "fix-deployment-image-tag" {
		t.Fatalf("unexpected id %q", got)
	}
}

func TestDeriveIDFallsBackForNonASCIISeed(t *testing.T) {
	got := DeriveID("修复 Deployment 镜像标签错误")
	if !strings.HasPrefix(got, "deployment") && !strings.HasPrefix(got, "challenge-") {
		t.Fatalf("unexpected id %q", got)
	}
	if got == "challenge" {
		t.Fatalf("unexpected generic fallback id %q", got)
	}
}

func TestDeriveIDFallsBackForPureNonASCIISeed(t *testing.T) {
	got := DeriveID("批量压缩旧日志")
	if !strings.HasPrefix(got, "challenge-") {
		t.Fatalf("unexpected id %q", got)
	}
	if len(got) <= len("challenge-") {
		t.Fatalf("unexpected short fallback id %q", got)
	}
}
