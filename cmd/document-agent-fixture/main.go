package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	app "github.com/breakfix/breakfix/internal/application/documentpractice"
	domain "github.com/breakfix/breakfix/internal/domain/documentpractice"
	"github.com/breakfix/breakfix/internal/domain/runnable"
)

type chatRequest struct {
	Model    string            `json:"model"`
	Messages []chatMessage     `json:"messages"`
	Tools    []json.RawMessage `json:"tools"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []choice `json:"choices"`
}

type choice struct {
	Index        int     `json:"index"`
	Message      message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

type message struct {
	Role      string     `json:"role"`
	Content   string     `json:"content"`
	ToolCalls []toolCall `json:"tool_calls,omitempty"`
}

type toolCall struct {
	Index    int          `json:"index"`
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function toolFunction `json:"function"`
}

type toolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

func main() {
	http.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	http.HandleFunc("/chat/completions", completions)
	http.HandleFunc("/v1/chat/completions", completions)
	log.Fatal(http.ListenAndServe(":8080", nil))
}

func completions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var request chatRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	toolName := requestedTool(request)
	arguments, err := resultArguments(toolName, request.Messages)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	response := chatResponse{
		ID: "documentation-fixture-" + fmt.Sprint(time.Now().UnixNano()), Object: "chat.completion", Created: time.Now().Unix(), Model: request.Model,
		Choices: []choice{{Index: 0, Message: message{Role: "assistant", ToolCalls: []toolCall{{Index: 0, ID: "fixture-call", Type: "function", Function: toolFunction{Name: toolName, Arguments: arguments}}}}, FinishReason: "tool_calls"}},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

func requestedTool(request chatRequest) string {
	for _, raw := range request.Tools {
		var tool struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		}
		if json.Unmarshal(raw, &tool) == nil && tool.Function.Name != "" {
			return tool.Function.Name
		}
	}
	return "submit_learning_unit_plan"
}

func lastPrompt(messages []chatMessage) string {
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role == "user" {
			return messages[index].Content
		}
	}
	return ""
}

func resultArguments(toolName string, messages []chatMessage) (string, error) {
	prompt := lastPrompt(messages)
	switch toolName {
	case "submit_learning_unit_plan":
		return planArguments(prompt)
	case "submit_document_review":
		return reviewArguments(prompt, messages)
	case "submit_candidate_blueprint":
		return candidateArguments(prompt)
	default:
		return "", fmt.Errorf("fixture does not implement tool %q", toolName)
	}
}

func decodePrompt(prompt string) (map[string]json.RawMessage, error) {
	var value map[string]json.RawMessage
	if err := json.Unmarshal([]byte(prompt), &value); err != nil {
		return nil, fmt.Errorf("fixture prompt is not JSON: %w", err)
	}
	return value, nil
}

func planArguments(prompt string) (string, error) {
	values, err := decodePrompt(prompt)
	if err != nil {
		return "", err
	}
	var page domain.Page
	var evidence []domain.EvidenceReference
	var constraints []domain.RuntimeConstraint
	if err := json.Unmarshal(values["page_metadata"], &page); err != nil {
		return "", err
	}
	if err := json.Unmarshal(values["evidence"], &evidence); err != nil {
		return "", err
	}
	if err := json.Unmarshal(values["allowed_runtime_constraints"], &constraints); err != nil || len(constraints) == 0 {
		return "", fmt.Errorf("fixture runtime constraint is missing")
	}
	plan := domain.LearningUnitPlan{FormatVersion: domain.FormatVersion, ID: "pod-lifecycle", Revision: 1, Context: page.Context, Title: "Observe Pod lifetime", Objective: "Observe a Pod reach Running", Boundary: "One Pod in the fixed Kubernetes environment", Runtime: constraints[0], Evidence: evidence, UserSteps: []domain.UserStep{{ID: "apply-pod", Instruction: "Create the Pod and observe its phase", EvidenceIDs: []string{"page"}}}, Observations: []domain.ObservationPoint{{ID: "pod-running", Description: "The Pod reaches the Running phase", EvidenceIDs: []string{"page"}}}, CreatedAt: time.Now().UTC()}
	return marshalValid(plan)
}

func reviewArguments(prompt string, messages []chatMessage) (string, error) {
	values, err := decodePrompt(prompt)
	if err != nil {
		return "", err
	}
	var runID string
	if err := json.Unmarshal(values["review_run_id"], &runID); err != nil {
		return "", err
	}
	role := "verification"
	for _, message := range messagesText(messages) {
		for _, candidate := range []string{"evidence", "value", "safety", "consistency", "verification"} {
			if strings.Contains(message, "独立 "+candidate+" 审核 Agent") {
				role = candidate
			}
		}
	}
	return marshalValid(domain.ReviewOpinion{ReviewerID: runID, Role: role, Decision: domain.ReviewApprove, PolicyVersion: "document-policy-v1"})
}

func messagesText(messages []chatMessage) []string {
	result := make([]string, 0, len(messages))
	for _, message := range messages {
		result = append(result, message.Content)
	}
	return result
}

func candidateArguments(prompt string) (string, error) {
	values, err := decodePrompt(prompt)
	if err != nil {
		return "", err
	}
	var plan domain.LearningUnitPlan
	var profile runnable.RuntimeProfile
	if err := json.Unmarshal(values["approved_plan"], &plan); err != nil {
		return "", err
	}
	if err := json.Unmarshal(values["server_resolved_runtime_profile"], &profile); err != nil {
		return "", err
	}
	// The workload image is a public, deterministic fixture image. The
	// server-resolved profile still controls the isolated terminal runtime.
	image := "busybox:1.36.1"
	target := runnable.TargetLocation{Kind: "management", ID: "cluster"}
	initialization := []runnable.ActionSpec{{ID: "initialize", Entrypoint: "scripts/init.sh", Target: target, BoundaryID: "management-write", TimeoutSeconds: 120, ExpectedExitCodes: []int{0}}}
	validation := runnable.ValidationPlan{FormatVersion: runnable.FormatVersion, Phases: []runnable.ValidationPhase{{ID: "observe", TimeoutSeconds: 240, Execution: runnable.PhaseSequential, Actions: []runnable.ActionSpec{{ID: "apply-pod", Entrypoint: "scripts/apply.sh", Target: target, BoundaryID: "management-write", TimeoutSeconds: 180, ExpectedExitCodes: []int{0}}}, Assertions: []runnable.AssertionSpec{{ID: "pod-running", Entrypoint: "scripts/assert.sh", Target: target, BoundaryID: "management-read", TimeoutSeconds: 60}}}}}
	blueprint := struct {
		ID             string                    `json:"id"`
		Revision       int64                     `json:"revision"`
		PlanID         string                    `json:"plan_id"`
		PlanRevision   int64                     `json:"plan_revision"`
		UserSteps      []domain.UserStep         `json:"user_steps,omitempty"`
		Observations   []domain.ObservationPoint `json:"observations,omitempty"`
		Initialization []runnable.ActionSpec     `json:"initialization"`
		ValidationPlan runnable.ValidationPlan   `json:"validation_plan"`
		Files          []app.GeneratedFile       `json:"files"`
	}{ID: "pod-lifecycle-e2e", Revision: 1, PlanID: plan.ID, PlanRevision: plan.Revision, UserSteps: plan.UserSteps, Observations: plan.Observations, Initialization: initialization, ValidationPlan: validation, Files: []app.GeneratedFile{
		{Path: "scripts/init.sh", Executable: true, Content: "#!/bin/sh\nset -eu\nkubectl delete pod pod-lifecycle --ignore-not-found --wait=true\n"},
		{Path: "scripts/apply.sh", Executable: true, Content: "#!/bin/sh\nset -eu\nkubectl run pod-lifecycle --image=" + image + " --restart=Never --command -- sleep 300\nkubectl wait --for=jsonpath='{.status.phase}'=Running pod/pod-lifecycle --timeout=180s\n"},
		{Path: "scripts/assert.sh", Executable: true, Content: "#!/bin/sh\nset -eu\nphase=$(kubectl get pod pod-lifecycle -o jsonpath='{.status.phase}')\nif [ \"$phase\" = Running ]; then satisfied=true; else satisfied=false; fi\nprintf '{\"assertions\":[{\"id\":\"pod-running\",\"satisfied\":%s,\"summary\":\"Pod phase is %s\"}]}\\n' \"$satisfied\" \"$phase\"\n"},
	}}
	return marshalValid(blueprint)
}

func marshalValid(value any) (string, error) {
	bytes, err := json.Marshal(value)
	return string(bytes), err
}
