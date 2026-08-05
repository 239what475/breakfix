package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	appassistant "github.com/breakfix/breakfix/internal/application/assistant"
	"github.com/breakfix/breakfix/internal/domain/agent"
)

const interactiveRunInterruptionReason = "server restarted before agent completion"

// RecoverInteractiveAgentRuns replaces only runs whose durable owner and input
// can still be reconstructed. Browser connections are not part of this
// recovery boundary: replacement runs execute under the Server lifecycle and
// persist their final conversation result for a later page reload.
func (h *Handler) RecoverInteractiveAgentRuns(ctx context.Context) error {
	if h == nil || h.db == nil {
		return nil
	}
	h.setRuntimeContext(ctx)
	if err := h.recoverAuthoringAgentRuns(ctx); err != nil {
		return err
	}
	return h.recoverAssistantAgentRuns(ctx)
}

func (h *Handler) recoverAuthoringAgentRuns(ctx context.Context) error {
	runs, err := h.db.Agent.ListActiveRunsForPurpose(ctx, "authoring")
	if err != nil {
		return fmt.Errorf("list active authoring runs: %w", err)
	}
	for _, run := range runs {
		if run.OwnerKind != "authoring-session" || strings.TrimSpace(run.OwnerRef) == "" || strings.TrimSpace(run.SessionID) == "" {
			if err := h.interruptInteractiveRun(ctx, run.ID, interactiveRunInterruptionReason+": invalid authoring owner"); err != nil {
				return err
			}
			continue
		}
		replacement, err := h.authoring.RestartInterruptedTurn(ctx, run.ID, interactiveRunInterruptionReason)
		if errors.Is(err, agent.ErrRunActive) || errors.Is(err, agent.ErrNotFound) {
			continue
		}
		if err != nil {
			return fmt.Errorf("replace interrupted authoring run %s: %w", run.ID, err)
		}
		if replacement == nil {
			continue
		}
		go h.continueRecoveredAuthoring(replacement.ID)
	}
	return nil
}

func (h *Handler) continueRecoveredAuthoring(runID string) {
	_, err := h.authoring.RunTurn(h.agentRuntimeContext(), runID, nil)
	if err != nil && h.agentRuntimeContext().Err() == nil {
		slog.Error("complete recovered authoring run", "run_id", runID, "err", err)
	}
}

func (h *Handler) recoverAssistantAgentRuns(ctx context.Context) error {
	runs, err := h.db.Agent.ListActiveRunsForPurpose(ctx, "assistant")
	if err != nil {
		return fmt.Errorf("list active assistant runs: %w", err)
	}
	for _, run := range runs {
		session, err := h.db.Agent.GetSession(ctx, run.SessionID)
		if err != nil {
			if interruptErr := h.interruptInteractiveRun(ctx, run.ID, interactiveRunInterruptionReason+": missing assistant session"); interruptErr != nil {
				return interruptErr
			}
			continue
		}
		request, err := h.recoveredAssistantRequest(ctx, *session, run)
		if err != nil {
			if interruptErr := h.interruptInteractiveRun(ctx, run.ID, interactiveRunInterruptionReason+": "+err.Error()); interruptErr != nil {
				return interruptErr
			}
			slog.Warn("do not restart assistant run without a current environment", "run_id", run.ID, "err", err)
			continue
		}
		replacement, err := h.assistant.RestartInterruptedTurn(ctx, run.ID, interactiveRunInterruptionReason)
		if errors.Is(err, agent.ErrRunActive) || errors.Is(err, agent.ErrNotFound) {
			continue
		}
		if err != nil {
			return fmt.Errorf("replace interrupted assistant run %s: %w", run.ID, err)
		}
		if replacement == nil {
			continue
		}
		go h.continueRecoveredAssistant(replacement.SessionID, replacement.ID, request)
	}
	return nil
}

func (h *Handler) continueRecoveredAssistant(sessionID, runID string, request appassistant.Request) {
	_, err := h.assistant.RunTurn(h.agentRuntimeContext(), sessionID, runID, request, nil)
	if err != nil && h.agentRuntimeContext().Err() == nil {
		slog.Error("complete recovered assistant run", "run_id", runID, "err", err)
	}
}

func (h *Handler) recoveredAssistantRequest(ctx context.Context, session agent.Session, run agent.Run) (appassistant.Request, error) {
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
	entry, err := h.catalog.Entry(ctx, environment.ChallengeRef)
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

func (h *Handler) interruptInteractiveRun(ctx context.Context, runID, reason string) error {
	err := h.db.Agent.InterruptRun(ctx, runID, reason, time.Now().UTC())
	if errors.Is(err, agent.ErrRunActive) || errors.Is(err, agent.ErrNotFound) {
		return nil
	}
	return err
}
