package runnable

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const MaxAssertionOutputBytes = 256 * 1024

// ParseAssertionOutput validates the strict stdout protocol shared by every
// read-only assertion executor. The expected ID is always selected from an
// immutable RunnableRevision, never supplied by an interactive caller.
func ParseAssertionOutput(raw []byte, expectedID string, outputs []ImmutableReference) (AssertionResult, error) {
	if len(raw) == 0 || len(raw) > MaxAssertionOutputBytes {
		return AssertionResult{}, errors.New("assertion output is empty or exceeds the protocol limit")
	}
	var document struct {
		Assertions []struct {
			ID        string `json:"id"`
			Satisfied *bool  `json:"satisfied"`
			Summary   string `json:"summary"`
			Details   string `json:"details,omitempty"`
		} `json:"assertions"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return AssertionResult{}, fmt.Errorf("assertion output is not strict JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return AssertionResult{}, errors.New("assertion output contains trailing JSON")
	}
	if len(document.Assertions) != 1 || document.Assertions[0].ID != expectedID || document.Assertions[0].Satisfied == nil {
		return AssertionResult{}, errors.New("assertion output must report its declared id exactly once")
	}
	result := AssertionResult{ID: document.Assertions[0].ID, Satisfied: *document.Assertions[0].Satisfied, Summary: document.Assertions[0].Summary, Details: document.Assertions[0].Details, Outputs: outputs}
	if err := result.Validate(); err != nil {
		return AssertionResult{}, err
	}
	return result, nil
}
