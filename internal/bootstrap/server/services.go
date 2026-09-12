package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	breakfixv1 "github.com/breakfix/breakfix/api/v1"
	"github.com/breakfix/breakfix/internal/adapter/incus"
	"github.com/breakfix/breakfix/internal/adapter/kubernetes"
	"github.com/breakfix/breakfix/internal/adapter/postgres"
	appgeneration "github.com/breakfix/breakfix/internal/application/generation"
	applearning "github.com/breakfix/breakfix/internal/application/learning"
	"github.com/breakfix/breakfix/internal/content/candidate"
	"github.com/breakfix/breakfix/internal/content/scenario"
	"github.com/breakfix/breakfix/internal/domain/execution"
	runtime "github.com/breakfix/breakfix/internal/domain/runtime"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// serviceLifecycle is a process lifecycle primitive, not a scheduler. The
// bootstrap lists every known Server service explicitly and waits for all of
// them before closing shared providers.
type serviceLifecycle struct {
	ctx    context.Context
	cancel context.CancelFunc
	group  sync.WaitGroup
	once   sync.Once
}

func newServiceLifecycle() *serviceLifecycle {
	ctx, cancel := context.WithCancel(context.Background())
	return &serviceLifecycle{ctx: ctx, cancel: cancel}
}

func (l *serviceLifecycle) start(name string, run func(context.Context) error) {
	if l == nil || run == nil {
		return
	}
	l.group.Add(1)
	go func() {
		defer l.group.Done()
		if err := run(l.ctx); err != nil && l.ctx.Err() == nil {
			// A background service logs its own pass failures. This log catches a
			// service that unexpectedly returned and would otherwise disappear.
			slog.Error("server background service stopped", "service", name, "err", err)
		}
	}()
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

func (s learningStore) RecordScenarioCompletion(ctx context.Context, userID, scenarioID, revisionID, environmentUID string, completedAt time.Time) error {
	return s.repository.RecordScenarioCompletion(ctx, userID, scenarioID, revisionID, environmentUID, completedAt)
}

func (s learningStore) FinishScenarioAttempt(ctx context.Context, environmentUID, outcome string, finishedAt time.Time) error {
	return s.repository.FinishScenarioAttempt(ctx, environmentUID, outcome, finishedAt)
}

type environmentProjectionSource struct {
	client    *kubernetes.Client
	namespace string
}

func (s environmentProjectionSource) ListEnvironmentProjections(ctx context.Context) ([]applearning.EnvironmentProjection, error) {
	if s.client == nil {
		return nil, errors.New("kubernetes client is required")
	}
	result := make([]applearning.EnvironmentProjection, 0)
	nodes, err := s.client.ListNodeEnvironments(ctx, s.namespace, "")
	if err != nil {
		return nil, err
	}
	for index := range nodes.Items {
		result = append(result, nodeProjection(nodes.Items[index]))
	}
	vk8s, err := s.client.ListVK8sEnvironments(ctx, s.namespace, "")
	if err != nil {
		return nil, err
	}
	for index := range vk8s.Items {
		result = append(result, vk8sProjection(vk8s.Items[index]))
	}
	return result, nil
}

func (s environmentProjectionSource) DeleteEnvironmentProjection(ctx context.Context, runtimeName, name string) error {
	if s.client == nil {
		return errors.New("kubernetes client is required")
	}
	switch scenario.NormalizeRuntime(runtimeName) {
	case scenario.RuntimeNode:
		return s.client.DeleteNodeEnvironment(ctx, s.namespace, name)
	case scenario.RuntimeK8s:
		return s.client.DeleteVK8sEnvironment(ctx, s.namespace, name)
	default:
		return fmt.Errorf("unsupported projected environment runtime %q", runtimeName)
	}
}

func nodeProjection(environment breakfixv1.NodeEnvironment) applearning.EnvironmentProjection {
	return environmentProjectionFromSpecStatus(string(environment.UID), environment.Name, scenario.RuntimeNode, environment.Spec.Environment, environment.Status.Environment)
}

func vk8sProjection(environment breakfixv1.VK8sEnvironment) applearning.EnvironmentProjection {
	return environmentProjectionFromSpecStatus(string(environment.UID), environment.Name, scenario.RuntimeK8s, environment.Spec.Environment, environment.Status.Environment)
}

func environmentProjectionFromSpecStatus(uid, name, runtimeName string, spec breakfixv1.EnvironmentSpec, status breakfixv1.EnvironmentStatus) applearning.EnvironmentProjection {
	checkpoints := make([]applearning.Checkpoint, 0)
	if status.Checkpoints != nil {
		checkpoints = make([]applearning.Checkpoint, 0, len(status.Checkpoints.Results))
		for _, checkpoint := range status.Checkpoints.Results {
			var firstPassedAt *time.Time
			if checkpoint.FirstPassedAt != nil && !checkpoint.FirstPassedAt.IsZero() {
				value := checkpoint.FirstPassedAt.UTC()
				firstPassedAt = &value
			}
			checkpoints = append(checkpoints, applearning.Checkpoint{ID: checkpoint.ID, FirstPassedAt: firstPassedAt, Summary: checkpoint.Summary})
		}
	}
	return applearning.EnvironmentProjection{
		UID: uid, Name: name, Runtime: runtimeName, Purpose: string(spec.Purpose), UserID: spec.UserRef,
		ScenarioID: spec.Source.Ref, ScenarioRevision: spec.Source.Revision, Phase: string(status.Phase),
		ReadyAt: timeValue(status.ReadyAt), CompletedAt: timeValue(status.CompletedAt), DestroyedAt: timeValue(status.DestroyedAt),
		Checkpoints: checkpoints,
	}
}

func timeValue(value *metav1.Time) *time.Time {
	if value == nil || value.IsZero() {
		return nil
	}
	result := value.UTC()
	return &result
}

type scenarioArtifactValidator struct {
	registryRepository string
	incusNamePrefix    string
}

func (v scenarioArtifactValidator) ValidateScenarioArtifact(action runtime.Context, artifact execution.ArtifactReference) error {
	if action.Artifact == nil || action.ScenarioID == "" || action.ScenarioRevisionID == "" {
		return errors.New("runtime action has no scenario publication input")
	}
	if err := artifact.Validate(action.Snapshot.Runtime); err != nil {
		return err
	}
	switch action.Snapshot.Runtime {
	case scenario.RuntimeK8s:
		expected, err := candidate.ScenarioOCIRepository(v.registryRepository, action.ScenarioID, action.ScenarioRevisionID)
		if err != nil {
			return fmt.Errorf("derive scenario OCI repository: %w", err)
		}
		actual, err := candidate.OCIRepository(artifact.OCIReference)
		if err != nil {
			return err
		}
		if actual != expected {
			return errors.New("scenario artifact OCI repository does not belong to publication")
		}
		stagingDigest, err := candidate.OCIDigest(action.Artifact.OCIReference)
		if err != nil {
			return fmt.Errorf("read candidate artifact digest: %w", err)
		}
		finalDigest, err := candidate.OCIDigest(artifact.OCIReference)
		if err != nil {
			return err
		}
		if finalDigest != stagingDigest {
			return errors.New("scenario artifact digest differs from verified candidate artifact")
		}
		return nil
	case scenario.RuntimeNode:
		expected, err := incus.AliasForScenario(v.incusNamePrefix, action.ScenarioID, action.ScenarioRevisionID)
		if err != nil {
			return fmt.Errorf("derive scenario Incus alias: %w", err)
		}
		if artifact.IncusAlias != expected || artifact.IncusFingerprint != action.Artifact.IncusFingerprint {
			return errors.New("scenario artifact does not match the verified Node artifact")
		}
		return nil
	default:
		return errors.New("runtime action has an unsupported scenario runtime")
	}
}

var _ applearning.CleanupRepository = learningStore{}
var _ applearning.ProjectionRepository = learningStore{}
var _ applearning.ProjectionSource = environmentProjectionSource{}
var _ appgeneration.ScenarioArtifactValidator = scenarioArtifactValidator{}
