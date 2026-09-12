package httpapi

import (
	"context"
	"errors"
	"fmt"

	breakfixv1 "github.com/breakfix/breakfix/api/v1"
	"github.com/breakfix/breakfix/internal/content/scenario"
)

var (
	errNoMatchingEnvironment = errors.New("no matching environment")
	errAmbiguousEnvironment  = errors.New("multiple scenario revisions have active environments")
)

// entryForEnvironment resolves the immutable content selected when an
// Environment was created. A later active revision must never replace the
// content, checkpoints, or assistant context of that Environment.
func (h *Handler) entryForEnvironment(ctx context.Context, environment *activeEnvironment) (*scenario.Entry, error) {
	if environment == nil || environment.ScenarioRef == "" || environment.SourceRevision == "" {
		return nil, errNoMatchingEnvironment
	}
	current, err := h.catalog.EntryOperations(ctx, environment.ScenarioRef)
	if err == nil && current.RevisionID == environment.SourceRevision {
		return current, nil
	}
	if err != nil && !errors.Is(err, scenario.ErrNotFound) {
		return nil, err
	}
	entry, err := h.catalog.HistoricalEntry(ctx, environment.ScenarioRef, environment.SourceRevision)
	if err != nil {
		return nil, err
	}
	if err := scenario.RequireOperationsScenario(entry); err != nil {
		return nil, scenario.ErrNotFound
	}
	return entry, nil
}

// entryForScenarioRevision keeps My Space projections on the same durable
// content boundary as active Environment operations. The current Catalog map
// is only an optimization for the active revision.
func (h *Handler) entryForScenarioRevision(ctx context.Context, catalogEntries map[string]scenario.Entry, scenarioID, revisionID string) (*scenario.Entry, error) {
	if entry, exists := catalogEntries[scenarioID]; exists && entry.RevisionID == revisionID {
		value := entry
		return &value, nil
	}
	return h.catalog.HistoricalEntry(ctx, scenarioID, revisionID)
}

// resolveEnvironmentScenario prefers the current Catalog revision. If that
// revision has no Environment, an existing historical Environment remains
// reachable only when it is unambiguous. The browser route has no revision
// parameter, so silently selecting one of several historical revisions would
// bind a terminal or assistant to the wrong task.
func (h *Handler) resolveEnvironmentScenario(ctx context.Context, userID, scenarioID string, includeCompleted bool) (*scenario.Entry, *activeEnvironment, error) {
	current, currentErr := h.catalog.EntryOperations(ctx, scenarioID)
	if currentErr == nil {
		var (
			environment *activeEnvironment
			err         error
		)
		if includeCompleted {
			environment, err = h.findProgressEnvironment(ctx, userID, current)
		} else {
			environment, err = h.findEnvironment(ctx, userID, current)
		}
		if err == nil {
			return current, environment, nil
		}
		if !errors.Is(err, errNoMatchingEnvironment) {
			return nil, nil, err
		}
	} else if !errors.Is(currentErr, scenario.ErrNotFound) {
		return nil, nil, currentErr
	}

	environment, err := h.findUniqueHistoricalEnvironment(ctx, userID, scenarioID, includeCompleted)
	if err != nil {
		return nil, nil, err
	}
	entry, err := h.entryForEnvironment(ctx, environment)
	if err != nil {
		return nil, nil, err
	}
	return entry, environment, nil
}

func (h *Handler) findUniqueHistoricalEnvironment(ctx context.Context, userID, scenarioID string, includeCompleted bool) (*activeEnvironment, error) {
	environments, err := h.listActiveEnvironments(ctx, userID)
	if err != nil {
		return nil, err
	}
	live := make([]activeEnvironment, 0, 1)
	completed := make([]activeEnvironment, 0, 1)
	for _, environment := range environments {
		if environment.ScenarioRef != scenarioID {
			continue
		}
		if isLiveEnvironmentPhase(environment.Phase) {
			live = append(live, environment)
			continue
		}
		if includeCompleted && environment.Phase == breakfixv1.EnvironmentCompleted {
			completed = append(completed, environment)
		}
	}
	if len(live) == 1 {
		return &live[0], nil
	}
	if len(live) > 1 {
		return nil, errAmbiguousEnvironment
	}
	if len(completed) == 1 {
		return &completed[0], nil
	}
	if len(completed) > 1 {
		return nil, errAmbiguousEnvironment
	}
	return nil, errNoMatchingEnvironment
}

func scenarioEnvironmentError(err error) error {
	if errors.Is(err, errAmbiguousEnvironment) {
		return fmt.Errorf("scenario has multiple active revisions; choose an environment from My Space")
	}
	return err
}
