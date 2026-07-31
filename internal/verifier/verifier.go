// Package verifier executes immutable candidates in the final Environment
// runtimes. It owns no workflow state and reports only through Server's fenced
// candidate API.
package verifier

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

	"github.com/breakfix/breakfix/internal/candidate"
	"github.com/breakfix/breakfix/internal/candidateworker"
	"github.com/breakfix/breakfix/internal/challenge"
	"github.com/breakfix/breakfix/internal/incusprovider"
	"github.com/breakfix/breakfix/internal/k8s"
	breakfixv1 "github.com/breakfix/breakfix/internal/k8s/apis/breakfix/v1"
	"github.com/breakfix/breakfix/internal/verification"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const (
	challengeRoot            = "/opt/breakfix/challenge"
	verificationOutputLimit  = 256 * 1024
	verificationPollInterval = time.Second
	cleanupTimeout           = 10 * time.Minute

	workItemAnnotation  = "breakfix.dev/work-item"
	attemptAnnotation   = "breakfix.dev/work-attempt"
	candidateAnnotation = "breakfix.dev/candidate-revision"
)

type EnvironmentClient interface {
	CreateNodeEnvironment(context.Context, string, *breakfixv1.NodeEnvironment) (*breakfixv1.NodeEnvironment, error)
	GetNodeEnvironment(context.Context, string, string) (*breakfixv1.NodeEnvironment, error)
	DeleteNodeEnvironmentWithUID(context.Context, string, string, types.UID) error
	CreateVK8sEnvironment(context.Context, string, *breakfixv1.VK8sEnvironment) (*breakfixv1.VK8sEnvironment, error)
	GetVK8sEnvironment(context.Context, string, string) (*breakfixv1.VK8sEnvironment, error)
	DeleteVK8sEnvironmentWithUID(context.Context, string, string, types.UID) error
	ExecInPodStreamsContext(context.Context, string, string, int, ...string) (k8s.PodExecResult, error)
}

type NodeExecutor interface {
	ExecNode(context.Context, incusprovider.ExecNodeRequest) (incusprovider.ExecNodeResult, error)
}

// VerificationReporter is the narrow Server handoff used by Verifier. The
// Worker owns failure/retry handling; Verifier only records an Environment and
// commits a successful verification report under its existing work-item fence.
type VerificationReporter interface {
	RecordVerificationEnvironment(context.Context, candidateworker.Claim, candidate.VerificationEnvironment) error
	CompleteVerification(context.Context, candidateworker.Claim, candidate.VerificationReport) error
}

type Executor struct {
	client       VerificationReporter
	environments EnvironmentClient
	node         NodeExecutor
	namespace    string
}

func NewExecutor(client VerificationReporter, environments EnvironmentClient, node NodeExecutor, namespace string) (*Executor, error) {
	if client == nil || environments == nil || strings.TrimSpace(namespace) == "" {
		return nil, errors.New("verifier requires Server and Environment clients plus a CRD namespace")
	}
	return &Executor{client: client, environments: environments, node: node, namespace: namespace}, nil
}

type environmentRef struct {
	runtime string
	name    string
	uid     types.UID
	node    *breakfixv1.NodeEnvironment
	vk8s    *breakfixv1.VK8sEnvironment
}

func (e *Executor) Execute(ctx context.Context, claim candidateworker.Claim) error {
	if claim.Candidate.Artifact == nil || claim.Work.Item.DeadlineAt == nil {
		return errors.New("verification candidate has no immutable artifact or deadline")
	}
	name := verification.EnvironmentName(claim.Work.Item.ID)
	if err := e.removePreviousEnvironment(ctx, claim, name); err != nil {
		return fmt.Errorf("remove previous verification environment: %w", err)
	}
	environment, err := e.createEnvironment(ctx, claim, name)
	if err != nil {
		return err
	}
	cleaned := false
	defer func() {
		if cleaned {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()
		if err := e.deleteAndWait(cleanupCtx, *environment); err != nil {
			slog.Error("clean verification environment", "work_item_id", claim.Work.Item.ID, "attempt", claim.Work.Item.Attempt, "environment_uid", environment.uid, "err", err)
		}
	}()

	identity := candidate.VerificationEnvironment{
		Runtime: claim.Candidate.Snapshot.Runtime, Name: environment.name, UID: string(environment.uid),
		WorkItemID: claim.Work.Item.ID, Attempt: int64(claim.Work.Item.Attempt),
	}
	if err := e.client.RecordVerificationEnvironment(ctx, claim, identity); err != nil {
		return err
	}
	ready, err := e.waitReady(ctx, *environment)
	if err != nil {
		if cleanupErr := e.deleteAndWait(ctx, *environment); cleanupErr == nil {
			cleaned = true
		}
		return err
	}

	report, verificationErr := e.runVerification(ctx, claim, ready)
	if err := e.deleteAndWait(ctx, ready); err != nil {
		return fmt.Errorf("delete verification environment: %w", err)
	}
	cleaned = true
	if verificationErr != nil {
		return verificationErr
	}
	return e.client.CompleteVerification(ctx, claim, report)
}

func (e *Executor) createEnvironment(ctx context.Context, claim candidateworker.Claim, name string) (*environmentRef, error) {
	common := breakfixv1.EnvironmentSpec{
		Purpose: breakfixv1.EnvironmentPurposeVerification,
		Source: breakfixv1.EnvironmentSourceSpec{
			Kind: breakfixv1.EnvironmentSourceCandidate, Ref: claim.Candidate.ID, Revision: claim.Candidate.ArchiveSHA256,
		},
		Checkpoints: environmentCheckpoints(claim.Candidate.Snapshot.Checkpoints),
		Lifecycle: breakfixv1.EnvironmentLifecycleSpec{
			DeadlineAt: &metav1.Time{Time: claim.Work.Item.DeadlineAt.UTC()},
		},
	}
	metadata := metav1.ObjectMeta{
		Name: name, Namespace: e.namespace,
		Labels: map[string]string{"breakfix.dev/purpose": string(breakfixv1.EnvironmentPurposeVerification)},
		Annotations: map[string]string{
			workItemAnnotation: claim.Work.Item.ID, attemptAnnotation: strconv.Itoa(claim.Work.Item.Attempt),
			candidateAnnotation: claim.Candidate.ID,
		},
	}
	switch claim.Candidate.Snapshot.Runtime {
	case challenge.RuntimeNode:
		snapshot := claim.Candidate.Snapshot.Node
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
					ImageFingerprint: claim.Candidate.Artifact.IncusFingerprint,
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
		snapshot := claim.Candidate.Snapshot.K8s
		if snapshot == nil {
			return nil, errors.New("K8s candidate has no runtime snapshot")
		}
		created, err := e.environments.CreateVK8sEnvironment(ctx, e.namespace, &breakfixv1.VK8sEnvironment{
			ObjectMeta: metadata,
			Spec: breakfixv1.VK8sEnvironmentSpec{
				Environment: common,
				Runtime: breakfixv1.VK8sRuntimeSnapshot{
					ImageDigest:     claim.Candidate.Artifact.OCIReference,
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
		return candidateworker.ArtifactFailure("RUNTIME_INIT_FAILED", summary, nil)
	}
	if failure.Class != breakfixv1.EnvironmentFailureInfrastructure {
		return errors.New("verification environment returned an unknown failure class")
	}
	return fmt.Errorf("verification environment infrastructure failure %s: %s", failure.Reason, summary)
}

func (e *Executor) runVerification(ctx context.Context, claim candidateworker.Claim, ref environmentRef) (candidate.VerificationReport, error) {
	switch ref.runtime {
	case challenge.RuntimeNode:
		return e.verifyNode(ctx, claim, ref)
	case challenge.RuntimeK8s:
		return e.verifyK8s(ctx, claim, ref)
	default:
		return candidate.VerificationReport{}, errors.New("candidate runtime is unsupported")
	}
}

func (e *Executor) verifyNode(ctx context.Context, claim candidateworker.Claim, ref environmentRef) (candidate.VerificationReport, error) {
	if e.node == nil {
		return candidate.VerificationReport{}, errors.New("node verifier is unavailable")
	}
	identity, err := nodeIdentity(ref.node)
	if err != nil {
		return candidate.VerificationReport{}, err
	}
	nodes := claim.Candidate.Snapshot.Node.Nodes
	answers, err := executeParallel(nodes, func(node candidate.NodeSnapshot) (candidate.ExecutionResult, error) {
		result, err := e.node.ExecNode(ctx, incusprovider.ExecNodeRequest{
			EnvironmentUID: string(ref.uid), Revision: claim.Candidate.ArchiveSHA256, Identity: identity,
			LogicalName: node.Name, Command: []string{"/bin/bash", path.Join(challengeRoot, "nodes", node.Name, "answer.sh")},
		})
		return candidate.ExecutionResult{Location: node.Name, ExitCode: result.ExitCode, Stdout: result.Stdout, Stderr: result.Stderr}, err
	})
	if err != nil {
		return candidate.VerificationReport{}, fmt.Errorf("execute Node answers: %w", err)
	}
	if hasFailedAnswer(answers) {
		report := failedReport(claim.Candidate.Snapshot, answers, "one or more answer scripts failed")
		return report, candidateworker.ArtifactFailure("ANSWER_FAILED", report.Summary, &report)
	}

	groups := nodeCheckpointGroups(claim.Candidate.Snapshot.Checkpoints)
	type checkRun struct {
		node    string
		results []candidate.CheckpointResult
	}
	checkNodes := make([]candidate.NodeSnapshot, 0, len(groups))
	for _, node := range nodes {
		if len(groups[node.Name]) > 0 {
			checkNodes = append(checkNodes, node)
		}
	}
	runs, err := executeParallel(checkNodes, func(node candidate.NodeSnapshot) (checkRun, error) {
		result, err := e.node.ExecNode(ctx, incusprovider.ExecNodeRequest{
			EnvironmentUID: string(ref.uid), Revision: claim.Candidate.ArchiveSHA256, Identity: identity,
			LogicalName: node.Name, Command: []string{"/bin/bash", path.Join(challengeRoot, "nodes", node.Name, "checks.sh")},
		})
		if err != nil {
			return checkRun{}, err
		}
		if result.ExitCode != 0 {
			return checkRun{}, &checkpointProtocolError{message: fmt.Sprintf("%s checks.sh exited with %d: %s", node.Name, result.ExitCode, strings.TrimSpace(result.Stderr))}
		}
		parsed, err := parseCheckpointResults(result.Stdout, groups[node.Name])
		if err != nil {
			return checkRun{}, &checkpointProtocolError{message: fmt.Sprintf("%s checks.sh: %v", node.Name, err)}
		}
		return checkRun{node: node.Name, results: parsed}, nil
	})
	if err != nil {
		var protocol *checkpointProtocolError
		if errors.As(err, &protocol) {
			report := failedReport(claim.Candidate.Snapshot, answers, protocol.Error())
			return report, candidateworker.ArtifactFailure("CHECKPOINT_PROTOCOL_FAILED", report.Summary, &report)
		}
		return candidate.VerificationReport{}, fmt.Errorf("execute Node checkpoints: %w", err)
	}
	byID := make(map[string]candidate.CheckpointResult)
	for _, run := range runs {
		for _, result := range run.results {
			byID[result.ID] = result
		}
	}
	checks := orderedCheckpointResults(claim.Candidate.Snapshot.Checkpoints, byID)
	return finishReport(claim.Candidate.Snapshot, answers, checks)
}

func (e *Executor) verifyK8s(ctx context.Context, claim candidateworker.Claim, ref environmentRef) (candidate.VerificationReport, error) {
	if ref.vk8s == nil || strings.TrimSpace(ref.vk8s.Status.Runtime.Namespace) == "" || strings.TrimSpace(ref.vk8s.Status.Runtime.TerminalPodName) == "" {
		return candidate.VerificationReport{}, errors.New("ready K8s verification environment has no terminal identity")
	}
	runtime := ref.vk8s.Status.Runtime
	answer, err := e.environments.ExecInPodStreamsContext(ctx, runtime.Namespace, runtime.TerminalPodName, verificationOutputLimit, "/bin/bash", path.Join(challengeRoot, "k8s", "answer.sh"))
	if err != nil {
		return candidate.VerificationReport{}, fmt.Errorf("execute K8s answer: %w", err)
	}
	answers := []candidate.ExecutionResult{{Location: "management", ExitCode: answer.ExitCode, Stdout: answer.Stdout, Stderr: answer.Stderr}}
	if answer.ExitCode != 0 {
		report := failedReport(claim.Candidate.Snapshot, answers, "K8s answer script failed")
		return report, candidateworker.ArtifactFailure("ANSWER_FAILED", report.Summary, &report)
	}
	check, err := e.environments.ExecInPodStreamsContext(ctx, runtime.Namespace, runtime.TerminalPodName, verificationOutputLimit, "/bin/bash", path.Join(challengeRoot, "k8s", "checks.sh"))
	if err != nil {
		return candidate.VerificationReport{}, fmt.Errorf("execute K8s checkpoints: %w", err)
	}
	if check.ExitCode != 0 {
		report := failedReport(claim.Candidate.Snapshot, answers, fmt.Sprintf("K8s checks.sh exited with %d: %s", check.ExitCode, strings.TrimSpace(check.Stderr)))
		return report, candidateworker.ArtifactFailure("CHECKPOINT_PROTOCOL_FAILED", report.Summary, &report)
	}
	checks, err := parseCheckpointResults(check.Stdout, claim.Candidate.Snapshot.Checkpoints)
	if err != nil {
		report := failedReport(claim.Candidate.Snapshot, answers, "K8s checks.sh: "+err.Error())
		return report, candidateworker.ArtifactFailure("CHECKPOINT_PROTOCOL_FAILED", report.Summary, &report)
	}
	return finishReport(claim.Candidate.Snapshot, answers, checks)
}

func finishReport(snapshot candidate.ExecutionSnapshot, answers []candidate.ExecutionResult, checks []candidate.CheckpointResult) (candidate.VerificationReport, error) {
	passed := !hasFailedAnswer(answers)
	for _, check := range checks {
		passed = passed && check.Passed
	}
	summary := "all answers and checkpoints passed"
	if !passed {
		summary = "one or more checkpoints did not pass"
	}
	report := candidate.VerificationReport{Passed: passed, Answers: answers, Checkpoints: checks, Summary: summary}
	if err := report.Validate(snapshot); err != nil {
		return candidate.VerificationReport{}, fmt.Errorf("construct verification report: %w", err)
	}
	if !passed {
		return report, candidateworker.ArtifactFailure("CHECKPOINTS_FAILED", summary, &report)
	}
	return report, nil
}

type checkpointProtocolError struct{ message string }

func (e *checkpointProtocolError) Error() string { return e.message }

func parseCheckpointResults(raw string, snapshots []candidate.CheckpointSnapshot) ([]candidate.CheckpointResult, error) {
	expected := make([]challenge.Checkpoint, len(snapshots))
	for index, checkpoint := range snapshots {
		expected[index] = challenge.Checkpoint{ID: checkpoint.ID, Node: checkpoint.Node}
	}
	report, err := challenge.ParseCheckReport(raw, expected)
	if err != nil {
		return nil, err
	}
	results := make([]candidate.CheckpointResult, len(report.Checks))
	for index, check := range report.Checks {
		results[index] = candidate.CheckpointResult{ID: check.ID, Passed: check.Passed, Summary: check.Summary, Details: check.Details}
	}
	return results, nil
}

func failedReport(snapshot candidate.ExecutionSnapshot, answers []candidate.ExecutionResult, summary string) candidate.VerificationReport {
	checks := make([]candidate.CheckpointResult, len(snapshot.Checkpoints))
	for index, checkpoint := range snapshot.Checkpoints {
		checks[index] = candidate.CheckpointResult{ID: checkpoint.ID, Passed: false, Summary: "not passed during verification", Details: summary}
	}
	return candidate.VerificationReport{Passed: false, Answers: answers, Checkpoints: checks, Summary: summary}
}

func hasFailedAnswer(results []candidate.ExecutionResult) bool {
	for _, result := range results {
		if result.ExitCode != 0 {
			return true
		}
	}
	return false
}

func nodeCheckpointGroups(checkpoints []candidate.CheckpointSnapshot) map[string][]candidate.CheckpointSnapshot {
	groups := make(map[string][]candidate.CheckpointSnapshot)
	for _, checkpoint := range checkpoints {
		groups[checkpoint.Node] = append(groups[checkpoint.Node], checkpoint)
	}
	return groups
}

func orderedCheckpointResults(snapshots []candidate.CheckpointSnapshot, values map[string]candidate.CheckpointResult) []candidate.CheckpointResult {
	result := make([]candidate.CheckpointResult, 0, len(snapshots))
	for _, checkpoint := range snapshots {
		if value, ok := values[checkpoint.ID]; ok {
			result = append(result, value)
		}
	}
	return result
}

func nodeIdentity(environment *breakfixv1.NodeEnvironment) (incusprovider.NodeEnvironmentIdentity, error) {
	if environment == nil || environment.UID == "" {
		return incusprovider.NodeEnvironmentIdentity{}, errors.New("ready Node verification environment is missing")
	}
	status := environment.Status.Runtime
	if strings.TrimSpace(status.Project) == "" || strings.TrimSpace(status.Network) == "" || strings.TrimSpace(status.ACL) == "" || strings.TrimSpace(status.Profile) == "" {
		return incusprovider.NodeEnvironmentIdentity{}, errors.New("ready Node verification environment has incomplete runtime identity")
	}
	identity := incusprovider.NodeEnvironmentIdentity{Project: status.Project, Network: status.Network, ACL: status.ACL, Profile: status.Profile}
	identity.Nodes = make([]incusprovider.NodeIdentity, len(status.Nodes))
	for index, node := range status.Nodes {
		identity.Nodes[index] = incusprovider.NodeIdentity{LogicalName: node.Name, InstanceName: node.InstanceName, Address: node.Address}
	}
	return identity, nil
}

func environmentCheckpoints(checkpoints []candidate.CheckpointSnapshot) []breakfixv1.EnvironmentCheckpointSpec {
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
		go func() {
			defer wait.Done()
			results[index], errorsByIndex[index] = execute(input)
		}()
	}
	wait.Wait()
	for _, err := range errorsByIndex {
		if err != nil {
			return nil, err
		}
	}
	return results, nil
}

func (e *Executor) removePreviousEnvironment(ctx context.Context, claim candidateworker.Claim, name string) error {
	if previous := claim.Candidate.VerifyEnvironment; previous != nil {
		if previous.Name != name || previous.WorkItemID != claim.Work.Item.ID || previous.Runtime != claim.Candidate.Snapshot.Runtime {
			return errors.New("recorded verification environment does not belong to this WorkItem")
		}
		return e.deleteAndWait(ctx, environmentRef{runtime: previous.Runtime, name: previous.Name, uid: types.UID(previous.UID)})
	}
	ref, err := e.findEnvironment(ctx, claim.Candidate.Snapshot.Runtime, name)
	if err != nil {
		return err
	}
	if ref == nil {
		return nil
	}
	var annotations map[string]string
	if ref.node != nil {
		annotations = ref.node.Annotations
	} else {
		annotations = ref.vk8s.Annotations
	}
	if annotations[workItemAnnotation] != claim.Work.Item.ID || annotations[candidateAnnotation] != claim.Candidate.ID {
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
		current, err := e.findEnvironment(ctx, ref.runtime, ref.name)
		if err != nil {
			return err
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
