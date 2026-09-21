package llm

import (
	"encoding/json"
	"strings"
	"testing"

	app "github.com/breakfix/breakfix/internal/application/documentpractice"
	"github.com/breakfix/breakfix/internal/bootstrap/config"
	domain "github.com/breakfix/breakfix/internal/domain/documentpractice"
	"github.com/breakfix/breakfix/internal/domain/runnable"
)

func TestNewDocumentReviewerRequiresRoleAndPolicyVersion(t *testing.T) {
	if _, err := NewDocumentReviewer(config.AgentConfig{}, "inventor", "policy-v1"); err == nil {
		t.Fatal("invented review role was accepted")
	}
	if _, err := NewDocumentReviewer(config.AgentConfig{}, "evidence", "  "); err == nil {
		t.Fatal("reviewer without a policy version was accepted")
	}
	reviewer, err := NewDocumentReviewer(config.AgentConfig{}, "evidence", " policy-v1 ")
	if err != nil {
		t.Fatalf("valid reviewer rejected: %v", err)
	}
	if reviewer.policyVersion != "policy-v1" {
		t.Fatalf("policy version = %q, want trimmed policy-v1", reviewer.policyVersion)
	}
}

func TestValidateReviewSubmissionFeedbackNamesExpectedValues(t *testing.T) {
	err := validateReviewSubmission(reviewSubmission{Decision: "approved"})
	if err == nil || !strings.Contains(err.Error(), `"approve"`) || !strings.Contains(err.Error(), `"reject"`) || !strings.Contains(err.Error(), `"approved"`) {
		t.Fatalf("decision rejection must name the expected values and the received one, got: %v", err)
	}
	if err := validateReviewSubmission(reviewSubmission{Decision: domain.ReviewReject}); err == nil {
		t.Fatal("reject without reasons was accepted")
	}
	if err := validateReviewSubmission(reviewSubmission{Decision: domain.ReviewApprove}); err != nil {
		t.Fatalf("approve without reasons rejected: %v", err)
	}
	if err := validateReviewSubmission(reviewSubmission{Decision: domain.ReviewReject, Reasons: []string{"evidence missing"}}); err != nil {
		t.Fatalf("reject with reasons rejected: %v", err)
	}
}

func TestReviewSubmissionToolSchemaConstrainsTheDecision(t *testing.T) {
	tool, err := NewResultTool[reviewSubmission]("submit_document_review", "提交文档实践审核意见。", validateReviewSubmission)
	if err != nil {
		t.Fatalf("derive review submission tool: %v", err)
	}
	info, err := tool.Info(t.Context())
	if err != nil {
		t.Fatalf("tool info: %v", err)
	}
	jsonSchema, err := info.ToJSONSchema()
	if err != nil {
		t.Fatalf("resolve tool schema: %v", err)
	}
	schema, err := json.Marshal(jsonSchema)
	if err != nil {
		t.Fatalf("marshal tool schema: %v", err)
	}
	for _, expected := range []string{`"approve"`, `"reject"`, `"decision"`} {
		if !strings.Contains(string(schema), expected) {
			t.Fatalf("tool schema is missing %s: %s", expected, schema)
		}
	}
	// The enum must not regress into a free-form string the provider cannot
	// constrain, and the model must never see protocol bookkeeping fields.
	for _, forbidden := range []string{"reviewer_id", "role", "policy_version"} {
		if strings.Contains(string(schema), forbidden) {
			t.Fatalf("tool schema exposes server-owned field %s: %s", forbidden, schema)
		}
	}
}

func TestReviewSubmissionToolRejectsBadDecisionWithActionableError(t *testing.T) {
	tool, err := NewResultTool[reviewSubmission]("submit_document_review", "提交文档实践审核意见。", validateReviewSubmission)
	if err != nil {
		t.Fatalf("derive review submission tool: %v", err)
	}
	result, err := tool.InvokableRun(t.Context(), `{"decision":"approved"}`)
	if err != nil {
		t.Fatalf("rejected submission must be a tool result, not an error: %v", err)
	}
	var payload struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("decode rejection payload: %v", err)
	}
	if payload.OK || !strings.Contains(payload.Error, `"approve"`) {
		t.Fatalf("rejection feedback must be actionable, got: %+v", payload)
	}
	if _, called := tool.Value(); called {
		t.Fatal("rejected submission must not register a value")
	}
	// The accepted path exits the agent through adk and only exists inside a
	// live agent run; payload acceptance is covered by validateReviewSubmission
	// and by the live documentation suites.
}

func TestPlannerPromptPayloadCarriesGateFeedbackAsProtocolData(t *testing.T) {
	evidence := []domain.EvidenceReference{{ID: "page", Kind: domain.EvidencePage, Path: "docs/pods.md", Digest: "sha256:" + strings.Repeat("a", 64)}}
	input, err := app.NewAgentInput(documentPlannerInstruction(), "# Pod lifecycle\nuntrusted body", evidence)
	if err != nil {
		t.Fatal(err)
	}
	constraints := []domain.RuntimeConstraint{{Runtime: "k8s", BaseImage: "kindest/node", Network: "isolated", Topology: "single-cluster"}}
	feedback := []app.GateFeedback{{Gate: "plan-gate", Attempt: 1, Reasons: []string{"evidence: rejected", "observation ungrounded"}}}
	payload, err := json.Marshal(documentPlannerPayload{
		DocumentData: input.DocumentData,
		Page:         domain.Page{Path: "docs/pods.md"},
		Evidence:     input.Evidence,
		Constraints:  constraints,
		Feedback:     feedback,
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(payload)
	// Feedback lands in its own protocol field, beside the Server-resolved
	// constraints, both of which survive the retry round trip.
	for _, expected := range []string{
		`"previous_gate_rejection_feedback":[{"gate":"plan-gate","attempt":1,"reasons":["evidence: rejected","observation ungrounded"]}]`,
		`"allowed_runtime_constraints":[{"runtime":"k8s","base_image":"kindest/node"`,
		`"untrusted_document_data":"# Pod lifecycle\nuntrusted body"`,
	} {
		if !strings.Contains(encoded, expected) {
			t.Fatalf("planner payload is missing %s: %s", expected, encoded)
		}
	}
	// The instruction channel never carries the feedback content or the
	// document text; only the payload field name is named in prose.
	instruction := documentPlannerInstruction()
	if strings.Contains(instruction, "observation ungrounded") || strings.Contains(instruction, "untrusted body") {
		t.Fatal("feedback or document text leaked into the planner instruction channel")
	}
}

func TestGeneratorPromptPayloadCarriesFeedbackBesideTheServerProfile(t *testing.T) {
	feedback := []app.GateFeedback{{Gate: "artifact-gate", Attempt: 2, Reasons: []string{"safety: rejected", "init script fetches http://example.com/payload"}}}
	payload, err := json.Marshal(documentGeneratorPayload{Profile: runnable.RuntimeProfile{Runtime: "k8s"}, Feedback: feedback})
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(payload)
	for _, expected := range []string{
		`"previous_gate_rejection_feedback":[{"gate":"artifact-gate","attempt":2,"reasons":["safety: rejected","init script fetches http://example.com/payload"]}]`,
		`"server_resolved_runtime_profile"`,
		`"approved_plan"`,
	} {
		if !strings.Contains(encoded, expected) {
			t.Fatalf("generator payload is missing %s: %s", expected, encoded)
		}
	}
	instruction := documentGeneratorInstruction()
	if strings.Contains(instruction, "http://example.com/payload") {
		t.Fatal("feedback content leaked into the generator instruction channel")
	}
}
