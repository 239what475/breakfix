package interactive

import (
	"context"
	"errors"
	"testing"
	"time"

	appassistant "github.com/breakfix/breakfix/internal/application/assistant"
	"github.com/breakfix/breakfix/internal/domain/agent"
)

func TestRecoveryInterruptsAuthoringWithoutStartingReplacement(t *testing.T) {
	repository := &recoveryRepository{authoringRuns: []agent.Run{{
		ID: "authoring-run", SessionID: "agent-session", Purpose: "authoring",
		OwnerKind: "authoring-session", OwnerRef: "authoring-session", Status: agent.RunRunning,
	}}}
	authoring := &recoveryAuthoringRuntime{}
	recovery, err := NewRecovery(RecoveryConfig{
		Repository: repository, Authoring: authoring,
		Assistant: recoveryAssistantRuntime{}, AssistantRequest: recoveryRequestRestorer{},
	})
	if err != nil {
		t.Fatalf("create recovery: %v", err)
	}
	if err := recovery.Recover(context.Background()); err != nil {
		t.Fatalf("recover interactive runs: %v", err)
	}
	if err := recovery.Recover(context.Background()); err != nil {
		t.Fatalf("repeat recovery: %v", err)
	}
	if len(authoring.interrupted) != 1 || authoring.interrupted[0] != "authoring-run" {
		t.Fatalf("interrupted authoring runs = %#v", authoring.interrupted)
	}
	if len(recovery.pending) != 0 {
		t.Fatalf("authoring recovery scheduled continuations: %#v", recovery.pending)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := recovery.Run(ctx); err != nil {
		t.Fatalf("run recovery lifecycle: %v", err)
	}
}

type recoveryRepository struct {
	authoringRuns []agent.Run
}

func (r *recoveryRepository) ListActiveRunsForPurpose(_ context.Context, purpose string) ([]agent.Run, error) {
	if purpose == "authoring" {
		return append([]agent.Run(nil), r.authoringRuns...), nil
	}
	return nil, nil
}

func (*recoveryRepository) GetSession(context.Context, string) (*agent.Session, error) {
	return nil, agent.ErrNotFound
}

func (*recoveryRepository) InterruptRun(context.Context, string, string, time.Time) error {
	return errors.New("unexpected generic interruption")
}

type recoveryAuthoringRuntime struct {
	interrupted []string
}

func (r *recoveryAuthoringRuntime) InterruptTurn(_ context.Context, runID string) error {
	r.interrupted = append(r.interrupted, runID)
	return nil
}

type recoveryAssistantRuntime struct{}

func (recoveryAssistantRuntime) RestartInterruptedTurn(context.Context, string, string) (*agent.Run, error) {
	return nil, errors.New("unexpected assistant restart")
}

func (recoveryAssistantRuntime) RunTurn(context.Context, string, string, appassistant.Request, func(appassistant.StreamEvent)) (appassistant.Message, error) {
	return appassistant.Message{}, errors.New("unexpected assistant execution")
}

type recoveryRequestRestorer struct{}

func (recoveryRequestRestorer) RestoreAssistantRequest(context.Context, agent.Session, agent.Run) (appassistant.Request, error) {
	return appassistant.Request{}, errors.New("unexpected assistant restoration")
}
