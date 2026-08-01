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
	"github.com/breakfix/breakfix/internal/domain/generation"

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

// Execute performs the Verifying phase. recordEnvironment is deliberately the
// only Server callback: the Generate Worker reports both it and the final
// result through the phase protocol under the workflow lease.
func (e *Executor) Execute(ctx context.Context, execution generation.Execution, recordEnvironment func(context.Context, generation.VerificationEnvironment) error) (generation.VerificationReport, error) {
	if !execution.Valid() || execution.Claim.Workflow.State != generation.StateVerifying || execution.Context.Candidate == nil {
		return generation.VerificationReport{}, errors.New("verifier requires a Verifying generation workflow with a candidate")
	}
	if recordEnvironment == nil {
		return generation.VerificationReport{}, errors.New("verifier requires an environment recorder")
	}
	view := execution.Context.Candidate
	if view.Artifact == nil || execution.Claim.Workflow.DeadlineAt == nil {
		return generation.VerificationReport{}, errors.New("verification candidate has no immutable artifact or deadline")
	}
	attempt := int64(execution.Claim.StateAttempt + 1)
	name := EnvironmentName(fmt.Sprintf("%s-%s-%d", execution.Claim.Workflow.ID, view.ID, attempt))
	if err := e.removePreviousEnvironment(ctx, execution, name); err != nil {
		return generation.VerificationReport{}, fmt.Errorf("remove previous verification environment: %w", err)
	}
	environment, err := e.createEnvironment(ctx, execution, name)
	if err != nil {
		return generation.VerificationReport{}, err
	}
	cleaned := false
	defer func() {
		if cleaned {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()
		if cleanupErr := e.deleteAndWait(cleanupCtx, *environment); cleanupErr != nil {
			slog.Error("clean verification environment", "workflow_id", execution.Claim.Workflow.ID, "attempt", attempt, "environment_uid", environment.uid, "err", cleanupErr)
		}
	}()

	identity := generation.VerificationEnvironment{
		Runtime: view.Snapshot.Runtime, Name: environment.name, UID: string(environment.uid),
		WorkflowID: execution.Claim.Workflow.ID, Attempt: attempt,
	}
	if err := recordEnvironment(ctx, identity); err != nil {
		return generation.VerificationReport{}, err
	}
	ready, err := e.waitReady(ctx, *environment)
	if err != nil {
		if cleanupErr := e.deleteAndWait(ctx, *environment); cleanupErr == nil {
			cleaned = true
		}
		return generation.VerificationReport{}, err
	}

	report, verificationErr := e.runVerification(ctx, *view, ready)
	if err := e.deleteAndWait(ctx, ready); err != nil {
		return generation.VerificationReport{}, fmt.Errorf("delete verification environment: %w", err)
	}
	cleaned = true
	if verificationErr != nil {
		return report, verificationErr
	}
	return report, nil
}

func (e *Executor) createEnvironment(ctx context.Context, execution generation.Execution, name string) (*environmentRef, error) {
	view := execution.Context.Candidate
	if view == nil || view.Artifact == nil || execution.Claim.Workflow.DeadlineAt == nil {
		return nil, errors.New("verification environment requires candidate artifact and deadline")
	}
	common := breakfixv1.EnvironmentSpec{
		Purpose: breakfixv1.EnvironmentPurposeVerification,
		Source: breakfixv1.EnvironmentSourceSpec{
			Kind: breakfixv1.EnvironmentSourceCandidate, Ref: view.ID, Revision: view.ArchiveSHA256,
		},
		Checkpoints: environmentCheckpoints(view.Snapshot.Checkpoints),
		Lifecycle: breakfixv1.EnvironmentLifecycleSpec{
			DeadlineAt: &metav1.Time{Time: execution.Claim.Workflow.DeadlineAt.UTC()},
		},
	}
	metadata := metav1.ObjectMeta{
		Name: name, Namespace: e.namespace,
		Labels: map[string]string{"breakfix.dev/purpose": string(breakfixv1.EnvironmentPurposeVerification)},
		Annotations: map[string]string{
			workflowAnnotation:        execution.Claim.Workflow.ID,
			workflowAttemptAnnotation: strconv.Itoa(execution.Claim.StateAttempt + 1),
			candidateAnnotation:       view.ID,
		},
	}
	switch view.Snapshot.Runtime {
	case challenge.RuntimeNode:
		snapshot := view.Snapshot.Node
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
					ImageFingerprint: view.Artifact.IncusFingerprint,
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
		snapshot := view.Snapshot.K8s
		if snapshot == nil {
			return nil, errors.New("K8s candidate has no runtime snapshot")
		}
		created, err := e.environments.CreateVK8sEnvironment(ctx, e.namespace, &breakfixv1.VK8sEnvironment{
			ObjectMeta: metadata,
			Spec: breakfixv1.VK8sEnvironmentSpec{
				Environment: common,
				Runtime: breakfixv1.VK8sRuntimeSnapshot{
					ImageDigest:     view.Artifact.OCIReference,
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
		return generation.NewArtifactError("RUNTIME_INIT_FAILED", summary)
	}
	if failure.Class != breakfixv1.EnvironmentFailureInfrastructure {
		return errors.New("verification environment returned an unknown failure class")
	}
	return fmt.Errorf("verification environment infrastructure failure %s: %s", failure.Reason, summary)
}

func (e *Executor) runVerification(ctx context.Context, view generation.WorkerView, ref environmentRef) (generation.VerificationReport, error) {
	switch ref.runtime {
	case challenge.RuntimeNode:
		return e.verifyNode(ctx, view, ref)
	case challenge.RuntimeK8s:
		return e.verifyK8s(ctx, view, ref)
	default:
		return generation.VerificationReport{}, errors.New("candidate runtime is unsupported")
	}
}

func (e *Executor) verifyNode(ctx context.Context, view generation.WorkerView, ref environmentRef) (generation.VerificationReport, error) {
	if e.node == nil {
		return generation.VerificationReport{}, errors.New("node verifier is unavailable")
	}
	if view.Snapshot.Node == nil {
		return generation.VerificationReport{}, errors.New("node verification candidate has no node snapshot")
	}
	identity, err := nodeIdentity(ref.node)
	if err != nil {
		return generation.VerificationReport{}, err
	}
	nodes := view.Snapshot.Node.Nodes
	answers, err := executeParallel(nodes, func(node generation.NodeSnapshot) (generation.ExecutionResult, error) {
		result, execErr := e.node.ExecNode(ctx, incus.ExecNodeRequest{
			EnvironmentUID: string(ref.uid), Revision: view.ArchiveSHA256, Identity: identity,
			LogicalName: node.Name, Command: []string{"/bin/bash", path.Join(challengeRoot, "nodes", node.Name, "answer.sh")},
		})
		return generation.ExecutionResult{Location: node.Name, ExitCode: result.ExitCode, Stdout: result.Stdout, Stderr: result.Stderr}, execErr
	})
	if err != nil {
		return generation.VerificationReport{}, fmt.Errorf("execute Node answers: %w", err)
	}
	if hasFailedAnswer(answers) {
		report := failedReport(view.Snapshot, answers, "one or more answer scripts failed")
		return report, generation.NewArtifactErrorWithReport("ANSWER_FAILED", report.Summary, report)
	}

	groups := nodeCheckpointGroups(view.Snapshot.Checkpoints)
	type checkRun struct {
		node    string
		results []generation.CheckpointResult
	}
	checkNodes := make([]generation.NodeSnapshot, 0, len(groups))
	for _, node := range nodes {
		if len(groups[node.Name]) > 0 {
			checkNodes = append(checkNodes, node)
		}
	}
	runs, err := executeParallel(checkNodes, func(node generation.NodeSnapshot) (checkRun, error) {
		result, execErr := e.node.ExecNode(ctx, incus.ExecNodeRequest{
			EnvironmentUID: string(ref.uid), Revision: view.ArchiveSHA256, Identity: identity,
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
			report := failedReport(view.Snapshot, answers, protocol.Error())
			return report, generation.NewArtifactErrorWithReport("CHECKPOINT_PROTOCOL_FAILED", report.Summary, report)
		}
		return generation.VerificationReport{}, fmt.Errorf("execute Node checkpoints: %w", err)
	}
	byID := make(map[string]generation.CheckpointResult)
	for _, run := range runs {
		for _, result := range run.results {
			byID[result.ID] = result
		}
	}
	checks := orderedCheckpointResults(view.Snapshot.Checkpoints, byID)
	return finishReport(view.Snapshot, answers, checks)
}

func (e *Executor) verifyK8s(ctx context.Context, view generation.WorkerView, ref environmentRef) (generation.VerificationReport, error) {
	if ref.vk8s == nil || strings.TrimSpace(ref.vk8s.Status.Runtime.Namespace) == "" || strings.TrimSpace(ref.vk8s.Status.Runtime.TerminalPodName) == "" {
		return generation.VerificationReport{}, errors.New("ready K8s verification environment has no terminal identity")
	}
	runtime := ref.vk8s.Status.Runtime
	answer, err := e.environments.ExecInPodStreamsContext(ctx, runtime.Namespace, runtime.TerminalPodName, verificationOutputLimit, "/bin/bash", path.Join(challengeRoot, "k8s", "answer.sh"))
	if err != nil {
		return generation.VerificationReport{}, fmt.Errorf("execute K8s answer: %w", err)
	}
	answers := []generation.ExecutionResult{{Location: "management", ExitCode: answer.ExitCode, Stdout: answer.Stdout, Stderr: answer.Stderr}}
	if answer.ExitCode != 0 {
		report := failedReport(view.Snapshot, answers, "K8s answer script failed")
		return report, generation.NewArtifactErrorWithReport("ANSWER_FAILED", report.Summary, report)
	}
	check, err := e.environments.ExecInPodStreamsContext(ctx, runtime.Namespace, runtime.TerminalPodName, verificationOutputLimit, "/bin/bash", path.Join(challengeRoot, "k8s", "checks.sh"))
	if err != nil {
		return generation.VerificationReport{}, fmt.Errorf("execute K8s checkpoints: %w", err)
	}
	if check.ExitCode != 0 {
		report := failedReport(view.Snapshot, answers, fmt.Sprintf("K8s checks.sh exited with %d: %s", check.ExitCode, strings.TrimSpace(check.Stderr)))
		return report, generation.NewArtifactErrorWithReport("CHECKPOINT_PROTOCOL_FAILED", report.Summary, report)
	}
	checks, err := parseCheckpointResults(check.Stdout, view.Snapshot.Checkpoints)
	if err != nil {
		report := failedReport(view.Snapshot, answers, "K8s checks.sh: "+err.Error())
		return report, generation.NewArtifactErrorWithReport("CHECKPOINT_PROTOCOL_FAILED", report.Summary, report)
	}
	return finishReport(view.Snapshot, answers, checks)
}

func finishReport(snapshot generation.ExecutionSnapshot, answers []generation.ExecutionResult, checks []generation.CheckpointResult) (generation.VerificationReport, error) {
	passed := !hasFailedAnswer(answers)
	for _, check := range checks {
		passed = passed && check.Passed
	}
	summary := "all answers and checkpoints passed"
	if !passed {
		summary = "one or more checkpoints did not pass"
	}
	report := generation.VerificationReport{Passed: passed, Answers: answers, Checkpoints: checks, Summary: summary}
	if err := report.Validate(snapshot); err != nil {
		return generation.VerificationReport{}, fmt.Errorf("construct verification report: %w", err)
	}
	if !passed {
		return report, generation.NewArtifactErrorWithReport("CHECKPOINTS_FAILED", summary, report)
	}
	return report, nil
}

type checkpointProtocolError struct{ message string }

func (e *checkpointProtocolError) Error() string { return e.message }

func parseCheckpointResults(raw string, snapshots []generation.CheckpointSnapshot) ([]generation.CheckpointResult, error) {
	expected := make([]challenge.Checkpoint, len(snapshots))
	for index, checkpoint := range snapshots {
		expected[index] = challenge.Checkpoint{ID: checkpoint.ID, Node: checkpoint.Node}
	}
	report, err := challenge.ParseCheckReport(raw, expected)
	if err != nil {
		return nil, err
	}
	results := make([]generation.CheckpointResult, len(report.Checks))
	for index, check := range report.Checks {
		results[index] = generation.CheckpointResult{ID: check.ID, Passed: check.Passed, Summary: check.Summary, Details: check.Details}
	}
	return results, nil
}

func failedReport(snapshot generation.ExecutionSnapshot, answers []generation.ExecutionResult, summary string) generation.VerificationReport {
	checks := make([]generation.CheckpointResult, len(snapshot.Checkpoints))
	for index, checkpoint := range snapshot.Checkpoints {
		checks[index] = generation.CheckpointResult{ID: checkpoint.ID, Passed: false, Summary: "not passed during verification", Details: summary}
	}
	return generation.VerificationReport{Passed: false, Answers: answers, Checkpoints: checks, Summary: summary}
}

func hasFailedAnswer(results []generation.ExecutionResult) bool {
	for _, result := range results {
		if result.ExitCode != 0 {
			return true
		}
	}
	return false
}

func nodeCheckpointGroups(checkpoints []generation.CheckpointSnapshot) map[string][]generation.CheckpointSnapshot {
	groups := make(map[string][]generation.CheckpointSnapshot)
	for _, checkpoint := range checkpoints {
		groups[checkpoint.Node] = append(groups[checkpoint.Node], checkpoint)
	}
	return groups
}

func orderedCheckpointResults(snapshots []generation.CheckpointSnapshot, values map[string]generation.CheckpointResult) []generation.CheckpointResult {
	result := make([]generation.CheckpointResult, 0, len(snapshots))
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

func environmentCheckpoints(checkpoints []generation.CheckpointSnapshot) []breakfixv1.EnvironmentCheckpointSpec {
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

func (e *Executor) removePreviousEnvironment(ctx context.Context, execution generation.Execution, name string) error {
	view := execution.Context.Candidate
	if view == nil {
		return errors.New("verification cleanup requires candidate")
	}
	if previous := view.VerifyEnvironment; previous != nil {
		if previous.WorkflowID != execution.Claim.Workflow.ID || previous.Runtime != view.Snapshot.Runtime {
			return errors.New("recorded verification environment does not belong to this workflow")
		}
		return e.deleteAndWait(ctx, environmentRef{runtime: previous.Runtime, name: previous.Name, uid: types.UID(previous.UID)})
	}
	ref, err := e.findEnvironment(ctx, view.Snapshot.Runtime, name)
	if err != nil || ref == nil {
		return err
	}
	var annotations map[string]string
	if ref.node != nil {
		annotations = ref.node.Annotations
	} else {
		annotations = ref.vk8s.Annotations
	}
	if annotations[workflowAnnotation] != execution.Claim.Workflow.ID || annotations[candidateAnnotation] != view.ID ||
		annotations[workflowAttemptAnnotation] != strconv.Itoa(execution.Claim.StateAttempt+1) {
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
