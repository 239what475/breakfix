package challenge

import "testing"

func TestParseCheckReportRequiresEveryDeclaredCheckpoint(t *testing.T) {
	checkpoints := []Checkpoint{
		{ID: "first", Title: "First", Description: "First check"},
		{ID: "second", Title: "Second", Description: "Second check"},
	}
	if _, err := ParseCheckReport(`{"checks":[{"id":"first","passed":true,"summary":"done","details":""}]}`, checkpoints); err == nil {
		t.Fatal("expected report with a missing checkpoint to fail")
	}
}

func TestParseCheckReportAggregatesAllDeclaredCheckpoints(t *testing.T) {
	checkpoints := []Checkpoint{
		{ID: "first", Title: "First", Description: "First check"},
		{ID: "second", Title: "Second", Description: "Second check"},
	}
	report, err := ParseCheckReport(`{"checks":[{"id":"first","passed":true,"summary":"done","details":""},{"id":"second","passed":false,"summary":"waiting","details":"fix it"}]}`, checkpoints)
	if err != nil {
		t.Fatalf("ParseCheckReport: %v", err)
	}
	if report.Passed() {
		t.Fatal("expected a report with a failed checkpoint not to pass")
	}
}
