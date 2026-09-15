// Package learning owns durable learning-history maintenance outside HTTP
// request handling.
package learning

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

const (
	defaultCleanupInterval     = time.Minute
	defaultHeartbeatTimeout    = 3 * time.Minute
	defaultConnectionRetention = 24 * time.Hour
	defaultProjectionInterval  = 2 * time.Second
	purposeLearning            = "learning"
	phaseCompleted             = "Completed"
	phaseDestroyed             = "Destroyed"
	phaseFailed                = "Failed"
	attemptOutcomeExpired      = "expired"
)

// CleanupRepository is the narrow persistence boundary for terminal activity
// retention. Environment lifecycle ownership remains with the controllers.
type CleanupRepository interface {
	CleanupTerminalActivity(context.Context, time.Time, time.Time) error
	DeleteClosedTerminalConnections(context.Context, time.Time) error
}

// CleanupService removes activity records left behind by a Server crash or a
// lost terminal close frame.
type CleanupService struct {
	repository CleanupRepository
	interval   time.Duration
	heartbeat  time.Duration
	retention  time.Duration
	now        func() time.Time
}

func NewCleanupService(repository CleanupRepository) (*CleanupService, error) {
	if repository == nil {
		return nil, errors.New("learning cleanup repository is required")
	}
	return &CleanupService{
		repository: repository,
		interval:   defaultCleanupInterval,
		heartbeat:  defaultHeartbeatTimeout,
		retention:  defaultConnectionRetention,
		now:        func() time.Time { return time.Now().UTC() },
	}, nil
}

// Recover performs the startup pass before Server readiness is exposed.
func (s *CleanupService) Recover(ctx context.Context) error { return s.RunOnce(ctx) }

func (s *CleanupService) RunOnce(ctx context.Context) error {
	if s == nil || s.repository == nil {
		return errors.New("learning cleanup service is not configured")
	}
	now := s.now().UTC()
	if err := s.repository.CleanupTerminalActivity(ctx, now.Add(-s.heartbeat), now); err != nil {
		return fmt.Errorf("cleanup terminal activity: %w", err)
	}
	if err := s.repository.DeleteClosedTerminalConnections(ctx, now.Add(-s.retention)); err != nil {
		return fmt.Errorf("cleanup terminal connection retention: %w", err)
	}
	return nil
}

// Run converges terminal activity until the Server lifecycle is cancelled.
func (s *CleanupService) Run(ctx context.Context) error {
	if s == nil {
		return errors.New("learning cleanup service is not configured")
	}
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := s.RunOnce(ctx); err != nil && ctx.Err() == nil {
				slog.Warn("cleanup learning terminal activity", "err", err)
			}
		}
	}
}

// CheckpointFirstPass is an immutable learning fact derived from an
// Environment status projection.
type CheckpointFirstPass struct {
	EnvironmentUID     string
	UserID             string
	ScenarioID         string
	ScenarioRevisionID string
	CheckpointID       string
	FirstPassedAt      time.Time
	Summary            string
}

// Checkpoint is the status subset required to project first-pass facts.
type Checkpoint struct {
	ID            string
	Passed        bool
	FirstPassedAt *time.Time
	Summary       string
}

// EnvironmentProjection is the stable application representation of one CRD
// status. Kubernetes conversion belongs to the adapter wired by bootstrap.
type EnvironmentProjection struct {
	UID              string
	Name             string
	Runtime          string
	Purpose          string
	UserID           string
	ScenarioID       string
	ScenarioRevision string
	Phase            string
	ReadyAt          *time.Time
	CompletedAt      *time.Time
	DestroyedAt      *time.Time
	Checkpoints      []Checkpoint
}

// ProjectionSource reads runtime Environment status and requests deletion
// after terminal learning facts have been persisted.
type ProjectionSource interface {
	ListEnvironmentProjections(context.Context) ([]EnvironmentProjection, error)
	DeleteEnvironmentProjection(context.Context, string, string, string) error
}

// ProjectionRepository is the durable learning history boundary. String
// outcomes keep this application package independent of PostgreSQL types.
type ProjectionRepository interface {
	RecordScenarioAttempt(context.Context, string, string, string, string, string, time.Time) error
	RecordCheckpointFirstPass(context.Context, CheckpointFirstPass) error
	ListCheckpointFirstPasses(context.Context, string) ([]CheckpointFirstPass, error)
	RecordScenarioCompletion(context.Context, string, string, string, string, time.Time) error
	FinishScenarioAttempt(context.Context, string, string, time.Time) error
}

// ProjectionService is the sole Server-side writer of learning history from
// controller-owned Environment status.
type ProjectionService struct {
	source     ProjectionSource
	repository ProjectionRepository
	interval   time.Duration
	now        func() time.Time
}

func NewProjectionService(source ProjectionSource, repository ProjectionRepository) (*ProjectionService, error) {
	if source == nil || repository == nil {
		return nil, errors.New("learning projection requires source and repository")
	}
	return &ProjectionService{
		source: source, repository: repository, interval: defaultProjectionInterval,
		now: func() time.Time { return time.Now().UTC() },
	}, nil
}

// Recover records any completed controller status before HTTP requests become
// available. Repeating a projection is intentionally idempotent.
func (s *ProjectionService) Recover(ctx context.Context) error { return s.RunOnce(ctx) }

func (s *ProjectionService) Run(ctx context.Context) error {
	if s == nil {
		return errors.New("learning projection service is not configured")
	}
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := s.RunOnce(ctx); err != nil && ctx.Err() == nil {
				slog.Warn("project learning environment statuses", "err", err)
			}
		}
	}
}

func (s *ProjectionService) RunOnce(ctx context.Context) error {
	if s == nil || s.source == nil || s.repository == nil {
		return errors.New("learning projection service is not configured")
	}
	projections, err := s.source.ListEnvironmentProjections(ctx)
	if err != nil {
		return fmt.Errorf("list environment projections: %w", err)
	}
	for _, projection := range projections {
		deleteAfter, err := s.Project(ctx, projection)
		if err != nil {
			return err
		}
		if !deleteAfter {
			continue
		}
		if err := s.source.DeleteEnvironmentProjection(ctx, projection.Runtime, projection.Name, projection.UID); err != nil {
			return fmt.Errorf("delete projected %s environment %q: %w", projection.Runtime, projection.Name, err)
		}
	}
	return nil
}

// Project persists one Environment's immutable learning facts. It reports
// whether its terminal CRD can be deleted after the durable projection.
func (s *ProjectionService) Project(ctx context.Context, projection EnvironmentProjection) (bool, error) {
	if s == nil || s.repository == nil {
		return false, errors.New("learning projection service is not configured")
	}
	if projection.Purpose != purposeLearning {
		return false, nil
	}
	if strings.TrimSpace(projection.UID) == "" {
		return false, fmt.Errorf("environment %q has no uid", projection.Name)
	}
	if projection.ReadyAt != nil && !projection.ReadyAt.IsZero() {
		if err := s.repository.RecordScenarioAttempt(ctx, projection.UserID, projection.ScenarioID, projection.ScenarioRevision, projection.UID, projection.Runtime, projection.ReadyAt.UTC()); err != nil {
			return false, fmt.Errorf("record environment attempt: %w", err)
		}
	}
	for _, checkpoint := range projection.Checkpoints {
		if !checkpoint.passed() || checkpoint.FirstPassedAt == nil || checkpoint.FirstPassedAt.IsZero() {
			continue
		}
		if err := s.repository.RecordCheckpointFirstPass(ctx, CheckpointFirstPass{
			EnvironmentUID: projection.UID, UserID: projection.UserID, ScenarioID: projection.ScenarioID,
			ScenarioRevisionID: projection.ScenarioRevision, CheckpointID: checkpoint.ID,
			FirstPassedAt: checkpoint.FirstPassedAt.UTC(), Summary: checkpoint.Summary,
		}); err != nil {
			return false, fmt.Errorf("record checkpoint first pass %q: %w", checkpoint.ID, err)
		}
	}
	passed, err := s.passedCheckpoints(ctx, projection)
	if err != nil {
		return false, err
	}
	if allCheckpointsPassed(projection.Checkpoints, passed) {
		if err := s.repository.RecordScenarioCompletion(ctx, projection.UserID, projection.ScenarioID, projection.ScenarioRevision, projection.UID, s.now().UTC()); err != nil {
			return false, fmt.Errorf("record checkpoint completion: %w", err)
		}
	}
	switch projection.Phase {
	case phaseCompleted:
		if err := s.repository.RecordScenarioCompletion(ctx, projection.UserID, projection.ScenarioID, projection.ScenarioRevision, projection.UID, s.lifecycleTime(projection.CompletedAt)); err != nil {
			return false, fmt.Errorf("record environment completion: %w", err)
		}
	case phaseDestroyed, phaseFailed:
		if projection.ReadyAt != nil && !projection.ReadyAt.IsZero() {
			if err := s.repository.FinishScenarioAttempt(ctx, projection.UID, attemptOutcomeExpired, s.lifecycleTime(projection.DestroyedAt)); err != nil {
				return false, fmt.Errorf("finish environment attempt: %w", err)
			}
		}
		return true, nil
	}
	return false, nil
}

func (c Checkpoint) passed() bool {
	return c.Passed || (c.FirstPassedAt != nil && !c.FirstPassedAt.IsZero())
}

func (s *ProjectionService) passedCheckpoints(ctx context.Context, projection EnvironmentProjection) (map[string]struct{}, error) {
	passed := make(map[string]struct{}, len(projection.Checkpoints))
	if len(projection.Checkpoints) == 0 {
		return passed, nil
	}
	for _, checkpoint := range projection.Checkpoints {
		if checkpoint.passed() {
			passed[checkpoint.ID] = struct{}{}
		}
	}
	events, err := s.repository.ListCheckpointFirstPasses(ctx, projection.UID)
	if err != nil {
		return nil, fmt.Errorf("list checkpoint first passes: %w", err)
	}
	for _, event := range events {
		passed[event.CheckpointID] = struct{}{}
	}
	return passed, nil
}

func allCheckpointsPassed(checkpoints []Checkpoint, passed map[string]struct{}) bool {
	if len(checkpoints) == 0 {
		return false
	}
	for _, checkpoint := range checkpoints {
		if _, exists := passed[checkpoint.ID]; !exists {
			return false
		}
	}
	return true
}

func (s *ProjectionService) lifecycleTime(value *time.Time) time.Time {
	if value != nil && !value.IsZero() {
		return value.UTC()
	}
	return s.now().UTC()
}
