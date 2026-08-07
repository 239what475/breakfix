package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	appassistant "github.com/breakfix/breakfix/internal/application/assistant"
	"github.com/breakfix/breakfix/internal/domain/agent"
)

// RestoreAssistantRequest implements the application startup-recovery port.
// It only rebuilds the HTTP-owned Environment reader; it never starts an
// AgentRun or changes durable state.
func (h *Handler) RestoreAssistantRequest(ctx context.Context, session agent.Session, run agent.Run) (appassistant.Request, error) {
	if h == nil {
		return appassistant.Request{}, errors.New("assistant request restorer is not configured")
	}
	if session.Purpose != "assistant" || session.OwnerKind != "environment" || run.Purpose != "assistant" ||
		run.OwnerKind != "environment" || run.OwnerRef != session.OwnerRef || run.SessionID != session.ID || strings.TrimSpace(session.UserRef) == "" {
		return appassistant.Request{}, errors.New("assistant run ownership is invalid")
	}
	input, err := decodeAssistantRunInput(run.Input)
	if err != nil {
		return appassistant.Request{}, err
	}
	environment, err := h.findActiveEnvironmentByUID(ctx, session.UserRef, run.OwnerRef)
	if err != nil {
		return appassistant.Request{}, err
	}
	entry, err := h.entryForEnvironment(ctx, environment)
	if err != nil {
		return appassistant.Request{}, err
	}
	return h.assistantRequestForEnvironment(ctx, session.UserRef, entry, environment, input)
}

func decodeAssistantRunInput(raw json.RawMessage) (appassistant.RunInput, error) {
	var input appassistant.RunInput
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return appassistant.RunInput{}, fmt.Errorf("decode assistant run input: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return appassistant.RunInput{}, errors.New("assistant run input has a second JSON document")
	} else if !errors.Is(err, io.EOF) {
		return appassistant.RunInput{}, fmt.Errorf("decode assistant run input suffix: %w", err)
	}
	return input, nil
}
