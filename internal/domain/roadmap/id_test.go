package roadmap_test

import (
	"regexp"
	"testing"

	"github.com/breakfix/breakfix/internal/domain/roadmap"
)

func TestSourceRefSegmentKeepsEnglishTitlesReadable(t *testing.T) {
	if got, want := roadmap.SourceRefSegment("Shell files and configuration"), "shell-files-and-configuration"; got != want {
		t.Fatalf("SourceRefSegment() = %q, want %q", got, want)
	}
}

func TestSourceRefSegmentCreatesStableASCIIReferenceForChineseTitles(t *testing.T) {
	first := roadmap.SourceRefSegment("日志清理脚本")
	second := roadmap.SourceRefSegment("日志清理脚本")
	if first != second {
		t.Fatalf("SourceRefSegment() is not deterministic: %q != %q", first, second)
	}
	if !regexp.MustCompile(`^item-[a-f0-9]{16}$`).MatchString(first) {
		t.Fatalf("SourceRefSegment() = %q, want an ASCII digest segment", first)
	}
	if got := roadmap.NewChallengeSourceRef("linux-operations/shell-files", "日志清理脚本"); got != "linux-operations/shell-files/"+first {
		t.Fatalf("NewChallengeSourceRef() = %q", got)
	}
}
