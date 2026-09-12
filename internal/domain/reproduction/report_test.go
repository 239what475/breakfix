package reproduction

import (
	"strings"
	"testing"
)

func TestParseReport(t *testing.T) {
	report, err := Parse(`{"evidence":[{"id":" initial-failure ","observed":true,"summary":" observed ","details":" broken "},{"id":"second","observed":false,"summary":"absent"}]}`, []string{"initial-failure", "second"})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Evidence) != 2 || report.Evidence[0] != (Evidence{ID: "initial-failure", Observed: true, Summary: "observed", Details: "broken"}) {
		t.Fatalf("report = %#v", report)
	}
	if report.Observed() {
		t.Fatal("report with an unobserved evidence item passed")
	}
	report.Evidence[1].Observed = true
	if !report.Observed() {
		t.Fatal("report with every evidence item observed did not pass")
	}
}

func TestParseReportRejectsInvalidProtocol(t *testing.T) {
	tests := map[string]struct {
		raw      string
		expected []string
		message  string
	}{
		"unknown id":      {raw: `{"evidence":[{"id":"other","observed":true,"summary":"observed"}]}`, expected: []string{"ready"}, message: "unknown id"},
		"duplicate id":    {raw: `{"evidence":[{"id":"ready","observed":true,"summary":"observed"},{"id":"ready","observed":true,"summary":"observed"}]}`, expected: []string{"ready"}, message: "duplicate id"},
		"missing id":      {raw: `{"evidence":[{"id":"first","observed":true,"summary":"observed"}]}`, expected: []string{"first", "second"}, message: "missing id"},
		"empty summary":   {raw: `{"evidence":[{"id":"ready","observed":true,"summary":"  "}]}`, expected: []string{"ready"}, message: "empty summary"},
		"no evidence":     {raw: `{"evidence":[]}`, expected: []string{"ready"}, message: "contains no evidence"},
		"invalid json":    {raw: `{"evidence":`, expected: []string{"ready"}, message: "parse reproduction report"},
		"no expected ids": {raw: `{"evidence":[]}`, message: "no reproduction evidence ids"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := Parse(test.raw, test.expected)
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("error = %v, want containing %q", err, test.message)
			}
		})
	}
}
