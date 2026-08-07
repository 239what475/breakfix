package checkpoint

import (
	"strings"
	"testing"
)

func TestParseReport(t *testing.T) {
	report, err := Parse(`{"checks":[{"id":" first ","passed":true,"summary":" ready ","details":" observed "},{"id":"second","passed":false,"summary":"waiting"}]}`, []string{"first", "second"})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Checks) != 2 || report.Checks[0] != (Result{ID: "first", Passed: true, Summary: "ready", Details: "observed"}) {
		t.Fatalf("report = %#v", report)
	}
	if report.Passed() {
		t.Fatal("report with a failed checkpoint passed")
	}
	report.Checks[1].Passed = true
	if !report.Passed() {
		t.Fatal("report with every checkpoint passing did not pass")
	}
}

func TestParseReportRejectsInvalidProtocol(t *testing.T) {
	tests := map[string]struct {
		raw      string
		expected []string
		message  string
	}{
		"unknown id":      {raw: `{"checks":[{"id":"other","passed":true,"summary":"ready"}]}`, expected: []string{"ready"}, message: "unknown id"},
		"duplicate id":    {raw: `{"checks":[{"id":"ready","passed":true,"summary":"ready"},{"id":"ready","passed":true,"summary":"ready"}]}`, expected: []string{"ready"}, message: "duplicate id"},
		"missing id":      {raw: `{"checks":[{"id":"first","passed":true,"summary":"ready"}]}`, expected: []string{"first", "second"}, message: "missing id"},
		"empty summary":   {raw: `{"checks":[{"id":"ready","passed":true,"summary":"  "}]}`, expected: []string{"ready"}, message: "empty summary"},
		"no checks":       {raw: `{"checks":[]}`, expected: []string{"ready"}, message: "contains no checks"},
		"invalid json":    {raw: `{"checks":`, expected: []string{"ready"}, message: "parse checkpoint report"},
		"no expected ids": {raw: `{"checks":[]}`, message: "no checkpoint ids"},
		"empty expected":  {raw: `{"checks":[]}`, expected: []string{" "}, message: "expected checkpoint id is empty"},
		"duplicate expected": {
			raw: `{"checks":[]}`, expected: []string{"ready", " ready "}, message: "expected checkpoint id \"ready\" is duplicated",
		},
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

func TestEmptyReportDoesNotPass(t *testing.T) {
	if (Report{}).Passed() {
		t.Fatal("empty report passed")
	}
}
