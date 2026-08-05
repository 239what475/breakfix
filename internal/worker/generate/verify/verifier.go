// Package verify executes an immutable candidate in the same Environment
// runtime used by learners. It owns neither scheduling nor Server state.
package verify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	breakfixv1 "github.com/breakfix/breakfix/api/v1"
	"github.com/breakfix/breakfix/internal/adapter/incus"
	"github.com/breakfix/breakfix/internal/adapter/kubernetes"
	"github.com/breakfix/breakfix/internal/content/challenge"
	domainexecution "github.com/breakfix/breakfix/internal/domain/execution"
	runtime "github.com/breakfix/breakfix/internal/domain/runtime"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const (
	challengeRoot            = "/opt/breakfix/challenge"
	verificationOutputLimit  = 256 * 1024
	verificationPollInterval = time.Second
	cleanupTimeout           = 10 * time.Minute

	workflowAnnotation        = "breakfix.dev/workflow"
	workflowAttemptAnnotation = "breakfix.dev/workflow-attempt"
	candidateAnnotation       = "breakfix.dev/candidate-revision"
)

type EnvironmentClient interface {
	CreateNodeEnvironment(context.Context, string, *breakfixv1.NodeEnvironment) (*breakfixv1.NodeEnvironment, error)
	GetNodeEnvironment(context.Context, string, string) (*breakfixv1.NodeEnvironment, error)
	DeleteNodeEnvironmentWithUID(context.Context, string, string, types.UID) error
	CreateVK8sEnvironment(context.Context, string, *breakfixv1.VK8sEnvironment) (*breakfixv1.VK8sEnvironment, error)
	GetVK8sEnvironment(context.Context, string, string) (*breakfixv1.VK8sEnvironment, error)
	DeleteVK8sEnvironmentWithUID(context.Context, string, string, types.UID) error
	ExecInPodStreamsContext(context.Context, string, string, int, ...string) (kubernetes.PodExecResult, error)
}

type NodeExecutor interface {
	ExecNode(context.Context, incus.ExecNodeRequest) (incus.ExecNodeResult, error)
}

type Executor struct {
	environments EnvironmentClient
	node         NodeExecutor
	namespace    string
}

func NewExecutor(environments EnvironmentClient, node NodeExecutor, namespace string) (*Executor, error) {
	if environments == nil || strings.TrimSpace(namespace) == "" {
		return nil, errors.New("verifier requires Environment client and CRD namespace")
	}
	return &Executor{environments: environments, node: node, namespace: namespace}, nil
}

type environmentRef struct {
	runtime string
	name    string
	uid     types.UID
	node    *breakfixv1.NodeEnvironment
	vk8s    *breakfixv1.VK8sEnvironment
}

// ExecuteWork verifies one immutable artifact without assuming that its
// owner is a GenerationWorkflow. The callback is the only persistence hook.
func (e *Executor) ExecuteWork(ctx context.Context, work domainexecution.Work, recordEnvironment func(context.Context, domainexecution.VerificationEnvironment) error) (domainexecution.VerificationReport, error) {
	if err := work.Validate(); err != nil {
		return domainexecution.VerificationReport{}, fmt.Errorf("verifier execution work: %w", err)
	}
	if recordEnvironment == nil {
		return domainexecution.VerificationReport{}, errors.New("verifier requires an environment recorder")
	}
	if work.Artifact == nil {
		return domainexecution.VerificationReport{}, errors.New("verification candidate has no immutable artifact")
	}
	name := EnvironmentName(fmt.Sprintf("%s-%s-%d", work.OwnerID, work.CandidateID, work.Attempt))
	if err := e.removePreviousEnvironment(ctx, work, name); err != nil {
		return domainexecution.VerificationReport{}, fmt.Errorf("remove previous verification environment: %w", err)
	}
	environment, err := e.createEnvironment(ctx, work, name)
	if err != nil {
		return domainexecution.VerificationReport{}, err
	}
	recorded := false
	defer func() {
		if recorded {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()
		if cleanupErr := e.deleteAndWait(cleanupCtx, *environment); cleanupErr != nil {
			slog.Error("clean verification environment", "owner_id", work.OwnerID, "attempt", work.Attempt, "environment_uid", environment.uid, "err", cleanupErr)
		}
	}()

	identity := domainexecution.VerificationEnvironment{
		Runtime: work.Snapshot.Runtime, Name: environment.name, UID: string(environment.uid),
		WorkflowID: work.OwnerID, Attempt: work.Attempt,
	}
	if err := recordEnvironment(ctx, identity); err != nil {
		return domainexecution.VerificationReport{}, err
	}
	recorded = true
	ready, err := e.waitReady(ctx, *environment)
	if err != nil {
		return domainexecution.VerificationReport{}, err
	}

	report, verificationErr := e.runVerification(ctx, work, ready)
	if verificationErr != nil {
		return report, verificationErr
	}
	return report, nil
}

// ReapVerificationEnvironment removes a persisted verification Environment
// after the workflow has recorded a report or become inactive. A running
// verification action never deletes its own Environment after recording it:
// that would make recovery race report persistence with provider cleanup.
func (e *Executor) ReapVerificationEnvironment(ctx context.Context, reap runtime.Reap) error {
	if reap.Valid() != nil || reap.Kind != runtime.ReapVerificationEnvironment {
		return errors.New("verification resource reap is invalid")
	}
	value := reap.VerificationEnvironment
	if value == nil {
		return nil
	}
	if err := value.Validate(reap.Snapshot.Runtime); err != nil {
		return err
	}
	return e.deleteAndWait(ctx, environmentRef{
		runtime: value.Runtime,
		name:    value.Name,
		uid:     types.UID(value.UID),
	})
}

func (e *Executor) createEnvironment(ctx context.Context, work domainexecution.Work, name string) (*environmentRef, error) {
	if work.Artifact == nil {
		return nil, errors.New("verification environment requires candidate artifact and deadline")
	}
	common := breakfixv1.EnvironmentSpec{
		Purpose: breakfixv1.EnvironmentPurposeVerification,
		Source: breakfixv1.EnvironmentSourceSpec{
			Kind: breakfixv1.EnvironmentSourceCandidate, Ref: work.CandidateID, Revision: work.ArchiveSHA256,
		},
		Checkpoints: environmentCheckpoints(work.Snapshot.Checkpoints),
		Lifecycle: breakfixv1.EnvironmentLifecycleSpec{
			DeadlineAt: &metav1.Time{Time: work.DeadlineAt.UTC()},
		},
	}
	metadata := metav1.ObjectMeta{
		Name: name, Namespace: e.namespace,
		Labels: map[string]string{"breakfix.dev/purpose": string(breakfixv1.EnvironmentPurposeVerification)},
		Annotations: map[string]string{
			workflowAnnotation:        work.OwnerID,
			workflowAttemptAnnotation: strconv.FormatInt(work.Attempt, 10),
			candidateAnnotation:       work.CandidateID,
		},
	}
	switch work.Snapshot.Runtime {
	case challenge.RuntimeNode:
		snapshot := work.Snapshot.Node
		if snapshot == nil {
			return nil, errors.New("node candidate has no runtime snapshot")
		}
		nodes := make([]breakfixv1.NodeRuntimeNodeSpec, len(snapshot.Nodes))
		for index, node := range snapshot.Nodes {
			nodes[index] = breakfixv1.NodeRuntimeNodeSpec{Name: node.Name, Title: node.Title}
		}
		created, err := e.environments.CreateNodeEnvironment(ctx, e.namespace, &breakfixv1.NodeEnvironment{
			ObjectMeta: metadata,
			Spec: breakfixv1.NodeEnvironmentSpec{
				Environment: common,
				Runtime: breakfixv1.NodeRuntimeSnapshot{
					ImageFingerprint: work.Artifact.IncusFingerprint,
					ProfileRevision:  snapshot.ProfileRevision, NetworkPolicyRevision: snapshot.NetworkPolicyRevision,
					Nodes: nodes,
					Resources: breakfixv1.NodeResourceSnapshot{
						CPU: snapshot.Resources.CPU, Memory: snapshot.Resources.Memory,
						Processes: snapshot.Resources.Processes, RootDisk: snapshot.Resources.RootDisk,
					},
				},
			},
		})
		if err != nil {
			return nil, fmt.Errorf("create Node verification environment: %w", err)
		}
		if created.UID == "" {
			return nil, errors.New("created Node verification environment has no UID")
		}
		return &environmentRef{runtime: challenge.RuntimeNode, name: name, uid: created.UID, node: created}, nil

	case challenge.RuntimeK8s:
		snapshot := work.Snapshot.K8s
		if snapshot == nil {
			return nil, errors.New("K8s candidate has no runtime snapshot")
		}
		created, err := e.environments.CreateVK8sEnvironment(ctx, e.namespace, &breakfixv1.VK8sEnvironment{
			ObjectMeta: metadata,
			Spec: breakfixv1.VK8sEnvironmentSpec{
				Environment: common,
				Runtime: breakfixv1.VK8sRuntimeSnapshot{
					ImageDigest:     work.Artifact.OCIReference,
					ProfileRevision: snapshot.ProfileRevision, Version: snapshot.Version,
					ManagementTerminalImage: snapshot.ManagementTerminalImage,
					Resources: breakfixv1.VK8sResourceSnapshot{
						ControlPlaneCPU: snapshot.Resources.ControlPlaneCPU, ControlPlaneMemory: snapshot.Resources.ControlPlaneMemory,
						ControlPlaneEphemeralStorage: snapshot.Resources.ControlPlaneEphemeralStorage,
						WorkloadCPU:                  snapshot.Resources.WorkloadCPU, WorkloadMemory: snapshot.Resources.WorkloadMemory,
						WorkloadEphemeralStorage: snapshot.Resources.WorkloadEphemeralStorage,
						QuotaCPU:                 snapshot.Resources.QuotaCPU, QuotaMemory: snapshot.Resources.QuotaMemory,
						QuotaEphemeralStorage: snapshot.Resources.QuotaEphemeralStorage,
					},
				},
			},
		})
		if err != nil {
			return nil, fmt.Errorf("create K8s verification environment: %w", err)
		}
		if created.UID == "" {
			return nil, errors.New("created K8s verification environment has no UID")
		}
		return &environmentRef{runtime: challenge.RuntimeK8s, name: name, uid: created.UID, vk8s: created}, nil
	default:
		return nil, errors.New("candidate runtime is unsupported")
	}
}

func (e *Executor) waitReady(ctx context.Context, ref environmentRef) (environmentRef, error) {
	for {
		switch ref.runtime {
		case challenge.RuntimeNode:
			environment, err := e.environments.GetNodeEnvironment(ctx, e.namespace, ref.name)
			if err != nil {
				return ref, err
			}
			if environment.UID != ref.uid {
				return ref, errors.New("node verification environment UID changed")
			}
			ref.node = environment
			if environment.Status.Environment.Phase == breakfixv1.EnvironmentReady {
				return ref, nil
			}
			if environment.Status.Environment.Phase == breakfixv1.EnvironmentFailed {
				return ref, environmentFailure(environment.Status.Environment.Failure)
			}
		case challenge.RuntimeK8s:
			environment, err := e.environments.GetVK8sEnvironment(ctx, e.namespace, ref.name)
			if err != nil {
				return ref, err
			}
			if environment.UID != ref.uid {
				return ref, errors.New("k8s verification environment UID changed")
			}
			ref.vk8s = environment
			if environment.Status.Environment.Phase == breakfixv1.EnvironmentReady {
				return ref, nil
			}
			if environment.Status.Environment.Phase == breakfixv1.EnvironmentFailed {
				return ref, environmentFailure(environment.Status.Environment.Failure)
			}
		}
		select {
		case <-ctx.Done():
			return ref, ctx.Err()
		case <-time.After(verificationPollInterval):
		}
	}
}

func environmentFailure(failure *breakfixv1.EnvironmentFailureStatus) error {
	if failure == nil {
		return errors.New("verification environment failed without structured failure status")
	}
	summary := strings.TrimSpace(failure.Message)
	if summary == "" {
		summary = strings.TrimSpace(failure.Reason)
	}
	if failure.Class == breakfixv1.EnvironmentFailureArtifact {
		return domainexecution.NewArtifactError("RUNTIME_INIT_FAILED", summary)
	}
	if failure.Class != breakfixv1.EnvironmentFailureInfrastructure {
		return errors.New("verification environment returned an unknown failure class")
	}
	return fmt.Errorf("verification environment infrastructure failure %s: %s", failure.Reason, summary)
}

func (e *Executor) runVerification(ctx context.Context, work domainexecution.Work, ref environmentRef) (domainexecution.VerificationReport, error) {
	switch ref.runtime {
	case challenge.RuntimeNode:
		return e.verifyNode(ctx, work, ref)
	case challenge.RuntimeK8s:
		return e.verifyK8s(ctx, work, ref)
	default:
		return domainexecution.VerificationReport{}, errors.New("candidate runtime is unsupported")
	}
}

func (e *Executor) verifyNode(ctx context.Context, work domainexecution.Work, ref environmentRef) (domainexecution.VerificationReport, error) {
	if e.node == nil {
		return domainexecution.VerificationReport{}, errors.New("node verifier is unavailable")
	}
	if work.Snapshot.Node == nil {
		return domainexecution.VerificationReport{}, errors.New("node verification candidate has no node snapshot")
	}
	identity, err := nodeIdentity(ref.node)
	if err != nil {
		return domainexecution.VerificationReport{}, err
	}
	nodes := work.Snapshot.Node.Nodes
	answers, err := executeParallel(nodes, func(node domainexecution.NodeSnapshot) (domainexecution.ExecutionResult, error) {
		result, execErr := e.node.ExecNode(ctx, incus.ExecNodeRequest{
			EnvironmentUID: string(ref.uid), Revision: work.ArchiveSHA256, Identity: identity,
			LogicalName: node.Name, Command: []string{"/bin/bash", path.Join(challengeRoot, "nodes", node.Name, "answer.sh")},
		})
		return domainexecution.ExecutionResult{Location: node.Name, ExitCode: result.ExitCode, Stdout: result.Stdout, Stderr: result.Stderr}, execErr
	})
	if err != nil {
		return domainexecution.VerificationReport{}, fmt.Errorf("execute Node answers: %w", err)
	}
	if hasFailedAnswer(answers) {
		report := failedReport(work.Snapshot, answers, "one or more answer scripts failed")
		return report, domainexecution.NewArtifactErrorWithReport("ANSWER_FAILED", report.Summary, report)
	}

	groups := nodeCheckpointGroups(work.Snapshot.Checkpoints)
	type checkRun struct {
		node    string
		results []domainexecution.CheckpointResult
	}
	checkNodes := make([]domainexecution.NodeSnapshot, 0, len(groups))
	for _, node := range nodes {
		if len(groups[node.Name]) > 0 {
			checkNodes = append(checkNodes, node)
		}
	}
	runs, err := executeParallel(checkNodes, func(node domainexecution.NodeSnapshot) (checkRun, error) {
		result, execErr := e.node.ExecNode(ctx, incus.ExecNodeRequest{
			EnvironmentUID: string(ref.uid), Revision: work.ArchiveSHA256, Identity: identity,
			LogicalName: node.Name, Command: []string{"/bin/bash", path.Join(challengeRoot, "nodes", node.Name, "checks.sh")},
		})
		if execErr != nil {
			return checkRun{}, execErr
		}
		if result.ExitCode != 0 {
			return checkRun{}, &checkpointProtocolError{message: fmt.Sprintf("%s checks.sh exited with %d: %s", node.Name, result.ExitCode, strings.TrimSpace(result.Stderr))}
		}
		parsed, parseErr := parseCheckpointResults(result.Stdout, groups[node.Name])
		if parseErr != nil {
			return checkRun{}, &checkpointProtocolError{message: fmt.Sprintf("%s checks.sh: %v", node.Name, parseErr)}
		}
		return checkRun{node: node.Name, results: parsed}, nil
	})
	if err != nil {
		var protocol *checkpointProtocolError
		if errors.As(err, &protocol) {
			report := failedReport(work.Snapshot, answers, protocol.Error())
			return report, domainexecution.NewArtifactErrorWithReport("CHECKPOINT_PROTOCOL_FAILED", report.Summary, report)
		}
		return domainexecution.VerificationReport{}, fmt.Errorf("execute Node checkpoints: %w", err)
	}
	byID := make(map[string]domainexecution.CheckpointResult)
	for _, run := range runs {
		for _, result := range run.results {
			byID[result.ID] = result
		}
	}
	checks := orderedCheckpointResults(work.Snapshot.Checkpoints, byID)
	return finishReport(work.Snapshot, answers, checks)
}

func (e *Executor) verifyK8s(ctx context.Context, work domainexecution.Work, ref environmentRef) (domainexecution.VerificationReport, error) {
	if ref.vk8s == nil || strings.TrimSpace(ref.vk8s.Status.Runtime.Namespace) == "" || strings.TrimSpace(ref.vk8s.Status.Runtime.TerminalPodName) == "" {
		return domainexecution.VerificationReport{}, errors.New("ready K8s verification environment has no terminal identity")
	}
	runtime := ref.vk8s.Status.Runtime
	answer, err := e.environments.ExecInPodStreamsContext(ctx, runtime.Namespace, runtime.TerminalPodName, verificationOutputLimit, "/bin/bash", path.Join(challengeRoot, "k8s", "answer.sh"))
	if err != nil {
		return domainexecution.VerificationReport{}, fmt.Errorf("execute K8s answer: %w", err)
	}
	answers := []domainexecution.ExecutionResult{{Location: "management", ExitCode: answer.ExitCode, Stdout: answer.Stdout, Stderr: answer.Stderr}}
	if answer.ExitCode != 0 {
		report := failedReport(work.Snapshot, answers, "K8s answer script failed")
		return report, domainexecution.NewArtifactErrorWithReport("ANSWER_FAILED", report.Summary, report)
	}
	check, err := e.environments.ExecInPodStreamsContext(ctx, runtime.Namespace, runtime.TerminalPodName, verificationOutputLimit, "/bin/bash", path.Join(challengeRoot, "k8s", "checks.sh"))
	if err != nil {
		return domainexecution.VerificationReport{}, fmt.Errorf("execute K8s checkpoints: %w", err)
	}
	if check.ExitCode != 0 {
		report := failedReport(work.Snapshot, answers, fmt.Sprintf("K8s checks.sh exited with %d: %s", check.ExitCode, strings.TrimSpace(check.Stderr)))
		return report, domainexecution.NewArtifactErrorWithReport("CHECKPOINT_PROTOCOL_FAILED", report.Summary, report)
	}
	checks, err := parseCheckpointResults(check.Stdout, work.Snapshot.Checkpoints)
	if err != nil {
		report := failedReport(work.Snapshot, answers, "K8s checks.sh: "+err.Error())
		return report, domainexecution.NewArtifactErrorWithReport("CHECKPOINT_PROTOCOL_FAILED", report.Summary, report)
	}
	return finishReport(work.Snapshot, answers, checks)
}

func finishReport(snapshot domainexecution.Snapshot, answers []domainexecution.ExecutionResult, checks []domainexecution.CheckpointResult) (domainexecution.VerificationReport, error) {
	passed := !hasFailedAnswer(answers)
	for _, check := range checks {
		passed = passed && check.Passed
	}
	summary := "all answers and checkpoints passed"
	if !passed {
		summary = "one or more checkpoints did not pass"
	}
	report := domainexecution.VerificationReport{Passed: passed, Answers: answers, Checkpoints: checks, Summary: summary}
	if err := report.Validate(snapshot); err != nil {
		return domainexecution.VerificationReport{}, fmt.Errorf("construct verification report: %w", err)
	}
	if !passed {
		return report, domainexecution.NewArtifactErrorWithReport("CHECKPOINTS_FAILED", summary, report)
	}
	return report, nil
}

type checkpointProtocolError struct{ message string }

func (e *checkpointProtocolError) Error() string { return e.message }

func parseCheckpointResults(raw string, snapshots []domainexecution.CheckpointSnapshot) ([]domainexecution.CheckpointResult, error) {
	expected := make([]challenge.Checkpoint, len(snapshots))
	for index, checkpoint := range snapshots {
		expected[index] = challenge.Checkpoint{ID: checkpoint.ID, Node: checkpoint.Node}
	}
	report, err := challenge.ParseCheckReport(raw, expected)
	if err != nil {
		return nil, err
	}
	results := make([]domainexecution.CheckpointResult, len(report.Checks))
	for index, check := range report.Checks {
		results[index] = domainexecution.CheckpointResult{ID: check.ID, Passed: check.Passed, Summary: check.Summary, Details: check.Details}
	}
	return results, nil
}

func failedReport(snapshot domainexecution.Snapshot, answers []domainexecution.ExecutionResult, summary string) domainexecution.VerificationReport {
	checks := make([]domainexecution.CheckpointResult, len(snapshot.Checkpoints))
	for index, checkpoint := range snapshot.Checkpoints {
		checks[index] = domainexecution.CheckpointResult{ID: checkpoint.ID, Passed: false, Summary: "not passed during verification", Details: summary}
	}
	return domainexecution.VerificationReport{Passed: false, Answers: answers, Checkpoints: checks, Summary: summary}
}

func hasFailedAnswer(results []domainexecution.ExecutionResult) bool {
	for _, result := range results {
		if result.ExitCode != 0 {
			return true
		}
	}
	return false
}

func nodeCheckpointGroups(checkpoints []domainexecution.CheckpointSnapshot) map[string][]domainexecution.CheckpointSnapshot {
	groups := make(map[string][]domainexecution.CheckpointSnapshot)
	for _, checkpoint := range checkpoints {
		groups[checkpoint.Node] = append(groups[checkpoint.Node], checkpoint)
	}
	return groups
}

func orderedCheckpointResults(snapshots []domainexecution.CheckpointSnapshot, values map[string]domainexecution.CheckpointResult) []domainexecution.CheckpointResult {
	result := make([]domainexecution.CheckpointResult, 0, len(snapshots))
	for _, checkpoint := range snapshots {
		if value, ok := values[checkpoint.ID]; ok {
			result = append(result, value)
		}
	}
	return result
}

func nodeIdentity(environment *breakfixv1.NodeEnvironment) (incus.NodeEnvironmentIdentity, error) {
	if environment == nil || environment.UID == "" {
		return incus.NodeEnvironmentIdentity{}, errors.New("ready Node verification environment is missing")
	}
	status := environment.Status.Runtime
	if strings.TrimSpace(status.Project) == "" || strings.TrimSpace(status.Network) == "" || strings.TrimSpace(status.ACL) == "" || strings.TrimSpace(status.Profile) == "" {
		return incus.NodeEnvironmentIdentity{}, errors.New("ready Node verification environment has incomplete runtime identity")
	}
	identity := incus.NodeEnvironmentIdentity{Project: status.Project, Network: status.Network, ACL: status.ACL, Profile: status.Profile}
	identity.Nodes = make([]incus.NodeIdentity, len(status.Nodes))
	for index, node := range status.Nodes {
		identity.Nodes[index] = incus.NodeIdentity{LogicalName: node.Name, InstanceName: node.InstanceName, Address: node.Address}
	}
	return identity, nil
}

func environmentCheckpoints(checkpoints []domainexecution.CheckpointSnapshot) []breakfixv1.EnvironmentCheckpointSpec {
	result := make([]breakfixv1.EnvironmentCheckpointSpec, len(checkpoints))
	for index, checkpoint := range checkpoints {
		result[index] = breakfixv1.EnvironmentCheckpointSpec{ID: checkpoint.ID, Node: checkpoint.Node}
	}
	return result
}

func executeParallel[I any, O any](inputs []I, execute func(I) (O, error)) ([]O, error) {
	results := make([]O, len(inputs))
	errorsByIndex := make([]error, len(inputs))
	var wait sync.WaitGroup
	for index, input := range inputs {
		wait.Add(1)
		go func(index int, input I) {
			defer wait.Done()
			results[index], errorsByIndex[index] = execute(input)
		}(index, input)
	}
	wait.Wait()
	for _, err := range errorsByIndex {
		if err != nil {
			return nil, err
		}
	}
	return results, nil
}

func (e *Executor) removePreviousEnvironment(ctx context.Context, work domainexecution.Work, name string) error {
	if previous := work.VerificationEnvironment; previous != nil {
		if previous.WorkflowID != work.OwnerID || previous.Runtime != work.Snapshot.Runtime || previous.Attempt <= 0 {
			return errors.New("recorded verification environment does not belong to this execution")
		}
		return e.deleteAndWait(ctx, environmentRef{runtime: previous.Runtime, name: previous.Name, uid: types.UID(previous.UID)})
	}
	ref, err := e.findEnvironment(ctx, work.Snapshot.Runtime, name)
	if err != nil || ref == nil {
		return err
	}
	var annotations map[string]string
	if ref.node != nil {
		annotations = ref.node.Annotations
	} else {
		annotations = ref.vk8s.Annotations
	}
	if annotations[workflowAnnotation] != work.OwnerID || annotations[candidateAnnotation] != work.CandidateID ||
		annotations[workflowAttemptAnnotation] != strconv.FormatInt(work.Attempt, 10) {
		return errors.New("existing verification environment has different ownership metadata")
	}
	return e.deleteAndWait(ctx, *ref)
}

func (e *Executor) findEnvironment(ctx context.Context, runtime, name string) (*environmentRef, error) {
	switch runtime {
	case challenge.RuntimeNode:
		environment, err := e.environments.GetNodeEnvironment(ctx, e.namespace, name)
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		return &environmentRef{runtime: runtime, name: name, uid: environment.UID, node: environment}, nil
	case challenge.RuntimeK8s:
		environment, err := e.environments.GetVK8sEnvironment(ctx, e.namespace, name)
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		return &environmentRef{runtime: runtime, name: name, uid: environment.UID, vk8s: environment}, nil
	default:
		return nil, errors.New("candidate runtime is unsupported")
	}
}

func (e *Executor) deleteAndWait(ctx context.Context, ref environmentRef) error {
	if ref.uid == "" {
		return errors.New("verification environment cleanup requires a UID")
	}
	var err error
	switch ref.runtime {
	case challenge.RuntimeNode:
		err = e.environments.DeleteNodeEnvironmentWithUID(ctx, e.namespace, ref.name, ref.uid)
	case challenge.RuntimeK8s:
		err = e.environments.DeleteVK8sEnvironmentWithUID(ctx, e.namespace, ref.name, ref.uid)
	default:
		return errors.New("verification environment has unsupported runtime")
	}
	if apierrors.IsConflict(err) || apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for {
		current, findErr := e.findEnvironment(ctx, ref.runtime, ref.name)
		if findErr != nil {
			return findErr
		}
		if current == nil || current.uid != ref.uid {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(verificationPollInterval):
		}
	}
}
