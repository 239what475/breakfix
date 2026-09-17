package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	runtimev2 "github.com/breakfix/breakfix/api/v2"
	"github.com/breakfix/breakfix/internal/adapter/kubernetes"
	"github.com/breakfix/breakfix/internal/adapter/postgres"
	applearning "github.com/breakfix/breakfix/internal/application/learning"
	"k8s.io/apimachinery/pkg/types"
)

// serviceLifecycle is a process lifecycle primitive, not a scheduler. The
// bootstrap lists every known Server service explicitly and waits for all of
// them before closing shared providers.
type serviceLifecycle struct {
	ctx      context.Context
	cancel   context.CancelFunc
	group    sync.WaitGroup
	once     sync.Once
	registry *serviceRegistry
}

func newServiceLifecycle() *serviceLifecycle {
	ctx, cancel := context.WithCancel(context.Background())
	return &serviceLifecycle{ctx: ctx, cancel: cancel, registry: newServiceRegistry()}
}

func (l *serviceLifecycle) start(name string, run func(context.Context) error) {
	if l == nil || run == nil {
		return
	}
	l.registry.register(name)
	l.group.Add(1)
	go func() {
		defer l.group.Done()
		if err := run(l.ctx); err != nil && l.ctx.Err() == nil {
			// A background service logs its own pass failures. This log catches a
			// service that unexpectedly returned and would otherwise disappear.
			slog.Error("server background service stopped", "service", name, "err", err)
			l.registry.recordError(name, err.Error())
		}
	}()
}

// tickObserver returns the reporter a background service calls after each of
// its passes. The function may be called concurrently; nil errors record a
// clean tick.
func (l *serviceLifecycle) tickObserver(name string) func(error) {
	return l.registry.tickObserver(name)
}

func (l *serviceLifecycle) statuses() []ServiceStatus {
	if l == nil || l.registry == nil {
		return nil
	}
	return l.registry.snapshot()
}

// serviceStatus is the in-memory observation of one background service. It is
// deliberately process-local: nothing here survives a restart, and no
// external component is contacted.
type ServiceStatus struct {
	Name       string     `json:"name"`
	StartedAt  time.Time  `json:"started_at"`
	LastTickAt *time.Time `json:"last_tick_at"`
	LastError  string     `json:"last_error"`
}

// serviceRegistry is the in-memory status table for the Server's background
// services. Bootstrap registers every service when it starts one; services
// report their ticks through the observer returned by tickObserver.
type serviceRegistry struct {
	mu       sync.Mutex
	services map[string]*ServiceStatus
}

func newServiceRegistry() *serviceRegistry {
	return &serviceRegistry{services: make(map[string]*ServiceStatus)}
}

func (r *serviceRegistry) register(name string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.services[name]; !exists {
		r.services[name] = &ServiceStatus{Name: name, StartedAt: time.Now().UTC()}
	}
}

func (r *serviceRegistry) tickObserver(name string) func(error) {
	return func(err error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		status, exists := r.services[name]
		if !exists {
			return
		}
		now := time.Now().UTC()
		status.LastTickAt = &now
		if err != nil {
			status.LastError = err.Error()
		}
	}
}

func (r *serviceRegistry) recordError(name, message string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if status, exists := r.services[name]; exists {
		status.LastError = message
	}
}

func (r *serviceRegistry) snapshot() []ServiceStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	statuses := make([]ServiceStatus, 0, len(r.services))
	for _, status := range r.services {
		copyValue := *status
		statuses = append(statuses, copyValue)
	}
	sort.Slice(statuses, func(i, j int) bool { return statuses[i].Name < statuses[j].Name })
	return statuses
}

func (l *serviceLifecycle) stop() {
	if l == nil {
		return
	}
	l.once.Do(func() {
		l.cancel()
		l.group.Wait()
	})
}

type learningStore struct {
	repository *postgres.EnvironmentRepository
}

func (s learningStore) CleanupTerminalActivity(ctx context.Context, staleBefore, now time.Time) error {
	return s.repository.CleanupTerminalActivity(ctx, staleBefore, now)
}

func (s learningStore) DeleteClosedTerminalConnections(ctx context.Context, before time.Time) error {
	return s.repository.DeleteClosedTerminalConnections(ctx, before)
}

func (s learningStore) RecordScenarioAttempt(ctx context.Context, userID, scenarioID, revisionID, environmentUID, runtimeName string, startedAt time.Time) error {
	return s.repository.RecordScenarioAttempt(ctx, userID, scenarioID, revisionID, environmentUID, runtimeName, startedAt)
}

func (s learningStore) RecordCheckpointFirstPass(ctx context.Context, event applearning.CheckpointFirstPass) error {
	return s.repository.RecordCheckpointFirstPass(ctx, postgres.CheckpointFirstPassEvent{
		EnvironmentUID: event.EnvironmentUID, UserID: event.UserID, ScenarioID: event.ScenarioID,
		ScenarioRevision: event.ScenarioRevisionID, CheckpointID: event.CheckpointID,
		FirstPassedAt: event.FirstPassedAt, Summary: event.Summary,
	})
}

func (s learningStore) ListCheckpointFirstPasses(ctx context.Context, environmentUID string) ([]applearning.CheckpointFirstPass, error) {
	events, err := s.repository.ListCheckpointFirstPasses(ctx, []string{environmentUID})
	if err != nil {
		return nil, err
	}
	result := make([]applearning.CheckpointFirstPass, 0, len(events[environmentUID]))
	for _, event := range events[environmentUID] {
		result = append(result, applearning.CheckpointFirstPass{
			EnvironmentUID: event.EnvironmentUID, UserID: event.UserID, ScenarioID: event.ScenarioID, ScenarioRevisionID: event.ScenarioRevision,
			CheckpointID: event.CheckpointID, FirstPassedAt: event.FirstPassedAt, Summary: event.Summary,
		})
	}
	return result, nil
}

func (s learningStore) RecordScenarioCompletion(ctx context.Context, userID, scenarioID, revisionID, environmentUID string, completedAt time.Time) error {
	return s.repository.RecordScenarioCompletion(ctx, userID, scenarioID, revisionID, environmentUID, completedAt)
}

func (s learningStore) FinishScenarioAttempt(ctx context.Context, environmentUID, outcome string, finishedAt time.Time) error {
	return s.repository.FinishScenarioAttempt(ctx, environmentUID, outcome, finishedAt)
}

type environmentProjectionSource struct {
	client    *kubernetes.Client
	namespace string
	evaluator operationsLearningEvaluator
}

func (s environmentProjectionSource) ListEnvironmentProjections(ctx context.Context) ([]applearning.EnvironmentProjection, error) {
	if s.client == nil {
		return nil, errors.New("kubernetes client is required")
	}
	environments, err := s.client.ListRuntimeEnvironments(ctx, s.namespace, "")
	if err != nil {
		return nil, err
	}
	result := make([]applearning.EnvironmentProjection, 0, len(environments.Items))
	for index := range environments.Items {
		environment := environments.Items[index]
		if projection, ok := runtimeProjection(environment); ok {
			if s.evaluator != nil && environment.Status.Phase == runtimev2.PhaseReady && environment.Spec.Purpose == runtimev2.PurposeLearning {
				checkpoints, err := s.evaluator.Evaluate(ctx, environment)
				if err != nil {
					return nil, fmt.Errorf("evaluate Operations learning checkpoints for %q: %w", environment.Name, err)
				}
				projection.Checkpoints = checkpoints
			}
			result = append(result, projection)
		}
	}
	return result, nil
}

func (s environmentProjectionSource) DeleteEnvironmentProjection(ctx context.Context, _ string, name, uid string) error {
	if s.client == nil {
		return errors.New("kubernetes client is required")
	}
	if name == "" || uid == "" {
		return errors.New("runtime environment projection has no stable identity")
	}
	return s.client.DeleteRuntimeEnvironmentWithUID(ctx, s.namespace, name, types.UID(uid))
}

func runtimeProjection(environment runtimev2.RuntimeEnvironment) (applearning.EnvironmentProjection, bool) {
	labels := environment.Labels
	if labels["breakfix.dev/content-kind"] != "operations" {
		return applearning.EnvironmentProjection{}, false
	}
	projection := applearning.EnvironmentProjection{
		UID: string(environment.UID), Name: environment.Name, Runtime: environment.Status.Runtime.Provider,
		Purpose: string(environment.Spec.Purpose), UserID: labels["breakfix.dev/user"],
		ScenarioID: labels["breakfix.dev/content-id"], ScenarioRevision: labels["breakfix.dev/content-revision"],
		Phase: string(environment.Status.Phase), Checkpoints: []applearning.Checkpoint{},
	}
	if environment.Status.Phase == runtimev2.PhaseReady || environment.Status.Phase == runtimev2.PhaseDraining || environment.Status.Phase == runtimev2.PhaseReleased {
		readyAt := environment.CreationTimestamp.UTC()
		if !readyAt.IsZero() {
			projection.ReadyAt = &readyAt
		}
	}
	if environment.Status.Phase == runtimev2.PhaseReleased {
		projection.Phase = "Destroyed"
		if environment.Status.Lifecycle.ReleasedAt != nil && !environment.Status.Lifecycle.ReleasedAt.IsZero() {
			destroyedAt := environment.Status.Lifecycle.ReleasedAt.UTC()
			projection.DestroyedAt = &destroyedAt
		}
	}
	return projection, true
}

var _ applearning.CleanupRepository = learningStore{}
var _ applearning.ProjectionRepository = learningStore{}
var _ applearning.ProjectionSource = environmentProjectionSource{}
