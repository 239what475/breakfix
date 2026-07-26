package agentmodel

import (
	"context"
	"strings"
	"testing"
)

type testResult struct {
	Decision string `json:"decision" jsonschema:"required,enum=pass,enum=reject"`
	Feedback string `json:"feedback" jsonschema:"required"`
}

func TestResultToolRejectsUnknownMissingAndTrailingJSON(t *testing.T) {
	tool, err := NewResultTool("submit_test_result", "submit a test result", func(value testResult) error {
		if value.Decision == "pass" && value.Feedback != "" {
			return errInvalidTestResult
		}
		if value.Decision == "reject" && value.Feedback == "" {
			return errInvalidTestResult
		}
		if value.Decision != "pass" && value.Decision != "reject" {
			return errInvalidTestResult
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, raw := range []string{
		`{"decision":"pass","feedback":"","unknown":true}`,
		`{"decision":"pass"}`,
		`{"decision":"pass","feedback":""} {}`,
		`{"decision":"reject","feedback":""}`,
	} {
		if _, err := tool.InvokableRun(context.Background(), raw); err == nil {
			t.Fatalf("InvokableRun(%s) succeeded", raw)
		}
	}
	if _, err := tool.InvokableRun(context.Background(), `{"decision":"pass","feedback":""}`); err != nil {
		t.Fatalf("valid typed result: %v", err)
	}
	if _, err := tool.InvokableRun(context.Background(), `{"decision":"pass","feedback":""}`); err == nil || !strings.Contains(err.Error(), ErrResultAlreadySubmitted.Error()) {
		t.Fatalf("second typed result error = %v", err)
	}
}

var errInvalidTestResult = &invalidTestResultError{}

type invalidTestResultError struct{}

func (*invalidTestResultError) Error() string { return "invalid test result" }
