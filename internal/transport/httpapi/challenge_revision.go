package httpapi

import (
	"context"
	"errors"
	"fmt"

	breakfixv1 "github.com/breakfix/breakfix/api/v1"
	"github.com/breakfix/breakfix/internal/content/challenge"
)

var (
	errNoMatchingEnvironment = errors.New("no matching environment")
	errAmbiguousEnvironment  = errors.New("multiple challenge revisions have active environments")
)

// entryForEnvironment resolves the immutable content selected when an
// Environment was created. A later active revision must never replace the
// content, checkpoints, or assistant context of that Environment.
func (h *Handler) entryForEnvironment(ctx context.Context, environment *activeEnvironment) (*challenge.Entry, error) {
	if environment == nil || environment.ChallengeRef == "" || environment.SourceRevision == "" {
		return nil, errNoMatchingEnvironment
	}
	current, err := h.catalog.Entry(ctx, environment.ChallengeRef)
	if err == nil && current.RevisionID == environment.SourceRevision {
		return current, nil
	}
	if err != nil && !errors.Is(err, challenge.ErrNotFound) {
		return nil, err
	}
	return h.catalog.HistoricalEntry(ctx, environment.ChallengeRef, environment.SourceRevision)
}

// entryForChallengeRevision keeps My Space projections on the same durable
// content boundary as active Environment operations. The current Catalog map
// is only an optimization for the active revision.
func (h *Handler) entryForChallengeRevision(ctx context.Context, catalogEntries map[string]challenge.Entry, challengeID, revisionID string) (*challenge.Entry, error) {
	if entry, exists := catalogEntries[challengeID]; exists && entry.RevisionID == revisionID {
		value := entry
		return &value, nil
	}
	return h.catalog.HistoricalEntry(ctx, challengeID, revisionID)
}

// resolveEnvironmentChallenge prefers the current Catalog revision. If that
// revision has no Environment, an existing historical Environment remains
// reachable only when it is unambiguous. The browser route has no revision
// parameter, so silently selecting one of several historical revisions would
// bind a terminal or assistant to the wrong task.
func (h *Handler) resolveEnvironmentChallenge(ctx context.Context, userID, challengeID string, includeCompleted bool) (*challenge.Entry, *activeEnvironment, error) {
	current, currentErr := h.catalog.Entry(ctx, challengeID)
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
	} else if !errors.Is(currentErr, challenge.ErrNotFound) {
		return nil, nil, currentErr
	}

	environment, err := h.findUniqueHistoricalEnvironment(ctx, userID, challengeID, includeCompleted)
	if err != nil {
		return nil, nil, err
	}
	entry, err := h.entryForEnvironment(ctx, environment)
	if err != nil {
		return nil, nil, err
	}
	return entry, environment, nil
}

func (h *Handler) findUniqueHistoricalEnvironment(ctx context.Context, userID, challengeID string, includeCompleted bool) (*activeEnvironment, error) {
	environments, err := h.listActiveEnvironments(ctx, userID)
	if err != nil {
		return nil, err
	}
	live := make([]activeEnvironment, 0, 1)
	completed := make([]activeEnvironment, 0, 1)
	for _, environment := range environments {
		if environment.ChallengeRef != challengeID {
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

func challengeEnvironmentNotFound(err error) bool {
	return errors.Is(err, errNoMatchingEnvironment) || errors.Is(err, challenge.ErrNotFound)
}

func challengeEnvironmentError(err error) error {
	if errors.Is(err, errAmbiguousEnvironment) {
		return fmt.Errorf("challenge has multiple active revisions; choose an environment from My Space")
	}
	return err
}
