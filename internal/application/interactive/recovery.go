// Package interactive owns startup recovery of direct Server AgentRuns.
package interactive

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	appassistant "github.com/breakfix/breakfix/internal/application/assistant"
	"github.com/breakfix/breakfix/internal/domain/agent"
)

const interruptionReason = "server restarted before agent completion"

// Repository is the durable startup-recovery boundary for direct interactive
// runs. Workflow-owned AgentRuns are recovered by their own application roles.
type Repository interface {
	ListActiveRunsForPurpose(context.Context, string) ([]agent.Run, error)
	GetSession(context.Context, string) (*agent.Session, error)
	InterruptRun(context.Context, string, string, time.Time) error
}

// AssistantRequestRestorer rebuilds the read-only environment tool boundary
// from durable run input. HTTP does not start recovery; it only implements
// this Server-assembly port because it owns the existing Environment adapter.
type AssistantRequestRestorer interface {
	RestoreAssistantRequest(context.Context, agent.Session, agent.Run) (appassistant.Request, error)
}

type AuthoringRuntime interface {
	InterruptTurn(context.Context, string) error
}

type AssistantRuntime interface {
	RestartInterruptedTurn(context.Context, string, string) (*agent.Run, error)
	RunTurn(context.Context, string, string, appassistant.Request, func(appassistant.StreamEvent)) (appassistant.Message, error)
}

type RecoveryConfig struct {
	Repository       Repository
	Authoring        AuthoringRuntime
	Assistant        AssistantRuntime
	AssistantRequest AssistantRequestRestorer
}

// Recovery only closes work that was owned by the previous Server process.
// Authoring never gets an automatic replacement run; the next author message
// starts it explicitly. Assistant recovery retains its existing continuation
// behavior because its request is independently reconstructible.
type Recovery struct {
	repository Repository
	authoring  AuthoringRuntime
	assistant  AssistantRuntime
	restorer   AssistantRequestRestorer
	now        func() time.Time
	pending    []continuation
	recovered  bool
}

type continuation struct {
	runID   string
	session string
	request appassistant.Request
}

func NewRecovery(config RecoveryConfig) (*Recovery, error) {
	if config.Repository == nil || config.Authoring == nil || config.Assistant == nil || config.AssistantRequest == nil {
		return nil, errors.New("interactive recovery requires repository, authoring, assistant, and request restorer")
	}
	return &Recovery{
		repository: config.Repository, authoring: config.Authoring, assistant: config.Assistant, restorer: config.AssistantRequest,
		now: func() time.Time { return time.Now().UTC() },
	}, nil
}

// Recover must complete before the Server accepts traffic. It is intentionally
// idempotent for one bootstrap instance so a failed assembly cannot create a
// second replacement turn.
func (r *Recovery) Recover(ctx context.Context) error {
	if r == nil {
		return errors.New("interactive recovery is not configured")
	}
	if r.recovered {
		return nil
	}
	if err := r.recoverAuthoring(ctx); err != nil {
		return err
	}
	if err := r.recoverAssistant(ctx); err != nil {
		return err
	}
	r.recovered = true
	return nil
}

func (r *Recovery) recoverAuthoring(ctx context.Context) error {
	runs, err := r.repository.ListActiveRunsForPurpose(ctx, "authoring")
	if err != nil {
		return fmt.Errorf("list active authoring runs: %w", err)
	}
	for _, run := range runs {
		if run.OwnerKind != "authoring-session" || strings.TrimSpace(run.OwnerRef) == "" || strings.TrimSpace(run.SessionID) == "" {
			if err := r.interrupt(ctx, run.ID, interruptionReason+": invalid authoring owner"); err != nil {
				return err
			}
			continue
		}
		if err := r.authoring.InterruptTurn(ctx, run.ID); err != nil && !errors.Is(err, agent.ErrRunActive) && !errors.Is(err, agent.ErrNotFound) {
			return fmt.Errorf("interrupt authoring run %s: %w", run.ID, err)
		}
	}
	return nil
}

func (r *Recovery) recoverAssistant(ctx context.Context) error {
	runs, err := r.repository.ListActiveRunsForPurpose(ctx, "assistant")
	if err != nil {
		return fmt.Errorf("list active assistant runs: %w", err)
	}
	for _, run := range runs {
		session, err := r.repository.GetSession(ctx, run.SessionID)
		if err != nil {
			if interruptErr := r.interrupt(ctx, run.ID, interruptionReason+": missing assistant session"); interruptErr != nil {
				return interruptErr
			}
			continue
		}
		request, err := r.restorer.RestoreAssistantRequest(ctx, *session, run)
		if err != nil {
			if interruptErr := r.interrupt(ctx, run.ID, interruptionReason+": "+err.Error()); interruptErr != nil {
				return interruptErr
			}
			slog.Warn("do not restart assistant run without a current environment", "run_id", run.ID, "err", err)
			continue
		}
		replacement, err := r.assistant.RestartInterruptedTurn(ctx, run.ID, interruptionReason)
		if errors.Is(err, agent.ErrRunActive) || errors.Is(err, agent.ErrNotFound) {
			continue
		}
		if err != nil {
			return fmt.Errorf("replace interrupted assistant run %s: %w", run.ID, err)
		}
		if replacement != nil {
			r.pending = append(r.pending, continuation{runID: replacement.ID, session: replacement.SessionID, request: request})
		}
	}
	return nil
}

// Run starts only Assistant replacements fixed by Recover, blocks until
// shutdown, and waits for every continuation before returning to bootstrap.
func (r *Recovery) Run(ctx context.Context) error {
	if r == nil {
		return errors.New("interactive recovery is not configured")
	}
	if !r.recovered {
		return errors.New("interactive recovery must complete before continuation")
	}
	var group sync.WaitGroup
	for _, value := range r.pending {
		value := value
		group.Add(1)
		go func() {
			defer group.Done()
			if _, err := r.assistant.RunTurn(ctx, value.session, value.runID, value.request, nil); err != nil && ctx.Err() == nil {
				slog.Error("complete recovered assistant run", "run_id", value.runID, "err", err)
			}
		}()
	}
	<-ctx.Done()
	group.Wait()
	return nil
}

func (r *Recovery) interrupt(ctx context.Context, runID, reason string) error {
	err := r.repository.InterruptRun(ctx, runID, reason, r.now())
	if errors.Is(err, agent.ErrRunActive) || errors.Is(err, agent.ErrNotFound) {
		return nil
	}
	return err
}
