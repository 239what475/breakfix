package documentpractice

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/domain/runnable"
)

func runnableRevisionRefForTest() runnable.RevisionReference {
	return runnable.RevisionReference{ID: "runnable-revision-01", Digest: "sha256:" + strings.Repeat("c", 64)}
}

func verificationReportRefForTest() runnable.VerificationReportReference {
	return runnable.VerificationReportReference{ID: "verification-report-01", Digest: "sha256:" + strings.Repeat("d", 64)}
}

func testRevision(t *testing.T, projection *ReaderProjection) PracticeRevision {
	t.Helper()
	return PracticeRevision{
		FormatVersion: FormatVersion,
		ID:            "practice-01",
		WorkflowID:    "workflow-01",
		Context: DocumentContext{
			FormatVersion: FormatVersion, SourceID: "kubernetes", Repository: "https://github.com/kubernetes/website",
			Commit: strings.Repeat("a", 40), Version: "v1.34", Language: "en", License: "CC BY 4.0",
			PagePath: "docs/pods.md", Anchor: "pod-lifecycle", ParserVersion: "docs-project-v10", PageDigest: "sha256:" + strings.Repeat("b", 64),
		},
		PlanID:                "plan-01",
		PlanRevision:          1,
		WorkflowRevision:      1,
		CandidateID:           "candidate-01",
		RunnableRevisionRef:   runnableRevisionRefForTest(),
		VerificationReportRef: verificationReportRefForTest(),
		PublicationManifestID: "manifest-01",
		ReaderProjection:      projection,
		PublishedAt:           time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC),
	}
}

// Revisions published before the reader projection existed must stay
// decodable: the projection is nullable and the reader read path filters
// records without one instead of migrating them.
func TestPracticeRevisionDecodesLegacyRevisionsWithoutReaderProjection(t *testing.T) {
	payload, err := json.Marshal(testRevision(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "reader_projection") {
		t.Fatalf("legacy revision unexpectedly marshaled a projection: %s", payload)
	}
	var decoded PracticeRevision
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.ReaderProjection != nil {
		t.Fatalf("legacy revision decoded with a reader projection: %+v", decoded.ReaderProjection)
	}
	if decoded.ID != "practice-01" || decoded.Context.Anchor != "pod-lifecycle" {
		t.Fatalf("legacy revision decoded incompletely: %#v", decoded)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatalf("legacy revision without a projection must still validate: %v", err)
	}
}

func TestPracticeRevisionRequiresProjectionTitleWhenPresent(t *testing.T) {
	revision := testRevision(t, &ReaderProjection{Objective: "Observe Pod state", Boundary: "One Pod", Steps: []string{}, Observations: []string{"Pod reaches Running"}})
	if err := revision.Validate(); err == nil {
		t.Fatal("reader projection without a title was accepted")
	}
	revision.ReaderProjection.Title = "Pod lifecycle"
	if err := revision.Validate(); err != nil {
		t.Fatalf("complete reader projection was rejected: %v", err)
	}
}

// The projection carries only instruction/description text: evidence bindings
// never leave the plan artifact.
func TestReaderProjectionFromPlanStripsEvidenceBindings(t *testing.T) {
	plan := LearningUnitPlan{
		FormatVersion: FormatVersion, ID: "plan-01", Revision: 1,
		Title: "Pod lifecycle", Objective: "Observe Pod state", Boundary: "One Pod",
		UserSteps: []UserStep{
			{ID: "apply-pod", Instruction: "Apply the Pod manifest", EvidenceIDs: []string{"page"}},
			{ID: "watch-pod", Instruction: "Watch the Pod become ready", EvidenceIDs: []string{"page"}},
		},
		Observations: []ObservationPoint{{ID: "phase", Description: "Pod reaches Running", EvidenceIDs: []string{"page"}}},
	}
	projection := ReaderProjectionFromPlan(plan)
	if projection == nil {
		t.Fatal("plan produced no reader projection")
	}
	if projection.Title != "Pod lifecycle" || projection.Objective != "Observe Pod state" || projection.Boundary != "One Pod" {
		t.Fatalf("projection header fields diverge from the plan: %+v", projection)
	}
	if len(projection.Steps) != 2 || projection.Steps[0] != "Apply the Pod manifest" || projection.Steps[1] != "Watch the Pod become ready" {
		t.Fatalf("projection steps are not the plan instructions: %+v", projection.Steps)
	}
	if len(projection.Observations) != 1 || projection.Observations[0] != "Pod reaches Running" {
		t.Fatalf("projection observations are not the plan descriptions: %+v", projection.Observations)
	}
	payload, err := json.Marshal(projection)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "evidence") {
		t.Fatalf("projection leaked evidence bindings: %s", payload)
	}
}
