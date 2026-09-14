package runnable

import (
	"strings"
	"testing"
	"time"
)

func TestStoredRevisionAndReportBindDigests(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 30, 0, 0, time.UTC)
	revision := validRevision(t, validSpec())
	revisionDigest, err := revision.Digest()
	if err != nil {
		t.Fatal(err)
	}
	storedRevision := StoredRevision{Reference: RevisionReference{ID: "revision-01", Digest: revisionDigest}, Revision: revision, CreatedAt: now}
	if err := storedRevision.Validate(); err != nil {
		t.Fatalf("validate stored revision: %v", err)
	}
	storedRevision.Reference.Digest = testDigest("e")
	if err := storedRevision.Validate(); err == nil || !strings.Contains(err.Error(), "invalid digest") {
		t.Fatalf("expected stored revision digest rejection, got %v", err)
	}

	report := validReport(revisionDigest)
	reportDigest, err := report.Digest(revision)
	if err != nil {
		t.Fatal(err)
	}
	storedReport := StoredVerificationReport{
		Reference: VerificationReportReference{ID: "report-01", Digest: reportDigest}, Report: report, RunnableRevision: revision, CreatedAt: now,
	}
	if err := storedReport.Validate(); err != nil {
		t.Fatalf("validate stored report: %v", err)
	}
	storedReport.Report.RunnableRevisionDigest = testDigest("f")
	if err := storedReport.Validate(); err == nil || !strings.Contains(err.Error(), "another runnable revision") {
		t.Fatalf("expected stored report revision rejection, got %v", err)
	}
}

func TestReapRequestFencesEnvironmentAndRevision(t *testing.T) {
	revision := validRevision(t, validSpec())
	digest, err := revision.Digest()
	if err != nil {
		t.Fatal(err)
	}
	request := ReapRequest{
		Namespace: "breakfix-system", Name: "environment-01", UID: "environment-uid", Revision: digest,
		Binding: EnvironmentBinding{Namespace: "breakfix-system", Name: "environment-01", UID: "environment-uid", RunnableRevision: revision},
	}
	if err := request.Valid(); err != nil {
		t.Fatalf("validate reap request: %v", err)
	}
	request.Binding.UID = "another-environment"
	if err := request.Valid(); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("expected reap binding fence rejection, got %v", err)
	}
}
