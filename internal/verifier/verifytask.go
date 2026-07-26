package verifier

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/challenge"
	"github.com/breakfix/breakfix/internal/k8s"
	breakfixv1 "github.com/breakfix/breakfix/internal/k8s/apis/breakfix/v1"
	"github.com/breakfix/breakfix/internal/verification"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
)

type Config struct {
	Kubeconfig       string
	RegistryAddr     string
	RegistryInsecure bool
	ServerURL        string
	InternalAPIKey   string
	VerifyTaskID     string
	VerifyTaskNS     string
	SubmissionID     string
}

func Run(ctx context.Context, cfg Config) error {
	if strings.TrimSpace(cfg.VerifyTaskID) == "" || strings.TrimSpace(cfg.SubmissionID) == "" {
		return fmt.Errorf("VERIFY_TASK_ID and VERIFY_SUBMISSION_ID are required")
	}
	verifyStart := time.Now()
	slog.Info("verify task started", "verifyTaskID", cfg.VerifyTaskID, "submissionID", cfg.SubmissionID, "namespace", cfg.VerifyTaskNS)
	client, err := k8s.New(cfg.Kubeconfig)
	if err != nil {
		return fmt.Errorf("k8s client: %w", err)
	}
	task, err := client.GetVerifyTask(ctx, cfg.VerifyTaskNS, cfg.VerifyTaskID)
	if err != nil {
		return fmt.Errorf("get verify task: %w", err)
	}
	if task.Status.Phase == breakfixv1.VerifyTaskSucceeded || task.Status.Phase == breakfixv1.VerifyTaskFailed {
		return nil
	}

	workDir, err := os.MkdirTemp("", "breakfix-verify-*")
	if err != nil {
		return fmt.Errorf("create temp workdir: %w", err)
	}
	defer os.RemoveAll(workDir) //nolint:errcheck

	downloadPath := filepath.Join(workDir, "artifact.tar.gz")
	stageStart := time.Now()
	slog.Info("verify stage start", "phase", "download_submission", "verifyTaskID", cfg.VerifyTaskID)
	if err := downloadSubmission(ctx, cfg, downloadPath); err != nil {
		return fmt.Errorf("download submission: %w", err)
	}
	slog.Info("verify stage done", "phase", "download_submission", "verifyTaskID", cfg.VerifyTaskID, "duration", time.Since(stageStart), "path", downloadPath)

	stageStart = time.Now()
	slog.Info("verify stage start", "phase", "extract_artifact", "verifyTaskID", cfg.VerifyTaskID)
	f, err := os.Open(downloadPath)
	if err != nil {
		return fmt.Errorf("open downloaded submission: %w", err)
	}
	defer f.Close()

	chalDir := filepath.Join(workDir, "challenge")
	if err := os.MkdirAll(chalDir, 0755); err != nil {
		return fmt.Errorf("create challenge workdir: %w", err)
	}
	if err := challenge.ExtractTarGz(chalDir, f); err != nil {
		return updateVerifyFailure(ctx, client, cfg.VerifyTaskNS, task, failureReport("ARTIFACT_EXTRACT_FAILED", err.Error()))
	}
	if _, err := challenge.ValidateSubmissionDir(chalDir); err != nil {
		return updateVerifyFailure(ctx, client, cfg.VerifyTaskNS, task, failureReport("STRUCTURE_INVALID", err.Error()))
	}
	challengeEntry, err := challenge.LoadSubmissionDir(chalDir)
	if err != nil {
		return updateVerifyFailure(ctx, client, cfg.VerifyTaskNS, task, failureReport("CHALLENGE_MANIFEST_INVALID", err.Error()))
	}
	slog.Info("verify stage done", "phase", "extract_artifact", "verifyTaskID", cfg.VerifyTaskID, "duration", time.Since(stageStart), "runtime", challengeEntry.Runtime)

	tempImage := verification.ImageName(cfg.RegistryAddr, cfg.VerifyTaskID)
	stageStart = time.Now()
	slog.Info("verify stage start", "phase", "build_image", "verifyTaskID", cfg.VerifyTaskID, "image", tempImage)
	if err := BuildAndPush(ctx, tempImage, chalDir, verifyBaseImage(tempImage, challengeEntry.Runtime), cfg.RegistryInsecure); err != nil {
		if isArtifactBuildError(err) {
			return updateVerifyFailure(ctx, client, cfg.VerifyTaskNS, task, failureReport("BUILD_FAILED", err.Error()))
		}
		return fmt.Errorf("build and push verification image: %w", err)
	}
	slog.Info("verify stage done", "phase", "build_image", "verifyTaskID", cfg.VerifyTaskID, "duration", time.Since(stageStart), "image", tempImage)

	stageStart = time.Now()
	slog.Info("verify stage start", "phase", "create_environment", "verifyTaskID", cfg.VerifyTaskID, "runtime", challengeEntry.Runtime)
	envRef, err := createVerifyEnvironment(ctx, client, cfg.VerifyTaskNS, cfg.VerifyTaskID, cfg.SubmissionID, challengeEntry, tempImage)
	if err != nil {
		return fmt.Errorf("create verification environment: %w", err)
	}
	slog.Info("verify stage done", "phase", "create_environment", "verifyTaskID", cfg.VerifyTaskID, "duration", time.Since(stageStart), "environment", envRef.Name, "runtime", envRef.Runtime)

	stageStart = time.Now()
	slog.Info("verify stage start", "phase", "wait_environment_ready", "verifyTaskID", cfg.VerifyTaskID, "environment", envRef.Name)
	readyEnv, err := waitVerifyEnvironmentReady(ctx, client, cfg.VerifyTaskNS, envRef, 15*time.Minute)
	if err != nil {
		return fmt.Errorf("wait for verification environment: %w", err)
	}
	slog.Info("verify stage done", "phase", "wait_environment_ready", "verifyTaskID", cfg.VerifyTaskID, "duration", time.Since(stageStart), "pod", readyEnv.PodName, "namespace", readyEnv.Namespace)

	stageStart = time.Now()
	slog.Info("verify stage start", "phase", "run_answer", "verifyTaskID", cfg.VerifyTaskID, "pod", readyEnv.PodName)
	ansExit, ansOut, err := client.ExecInPod(readyEnv.Namespace, readyEnv.PodName, "bash", "/answer.sh")
	if err != nil {
		return fmt.Errorf("run answer.sh: %w", err)
	}
	slog.Info("verify stage done", "phase", "run_answer", "verifyTaskID", cfg.VerifyTaskID, "duration", time.Since(stageStart), "exitCode", ansExit)
	if ansExit != 0 {
		return updateVerifyFailure(ctx, client, cfg.VerifyTaskNS, task, &breakfixv1.VerifyReport{
			BuildPassed: true,
			Summary:     fmt.Sprintf("answer.sh exit=%d", ansExit),
			Issues: []breakfixv1.VerifyIssue{{
				Code:    "ANSWER_EXIT_NONZERO",
				Message: truncateStr(ansOut, 4000),
			}},
		})
	}

	stageStart = time.Now()
	slog.Info("verify stage start", "phase", "run_checkpoints", "verifyTaskID", cfg.VerifyTaskID, "pod", readyEnv.PodName)
	checkExit, checkOut, err := client.ExecInPod(readyEnv.Namespace, readyEnv.PodName, challenge.CheckpointCommand, "--json")
	if err != nil {
		return fmt.Errorf("run checkpoints: %w", err)
	}
	if checkExit != 0 {
		return updateVerifyFailure(ctx, client, cfg.VerifyTaskNS, task, failureReport("CHECKPOINT_PROTOCOL_FAILED", fmt.Sprintf("checkpoint runner exit=%d: %s", checkExit, truncateStr(checkOut, 4000))))
	}
	report, err := challenge.ParseCheckReport(checkOut, challengeEntry.Checkpoints)
	if err != nil {
		return updateVerifyFailure(ctx, client, cfg.VerifyTaskNS, task, failureReport("CHECKPOINT_REPORT_INVALID", fmt.Sprintf("%v; output=%q", err, truncateStr(checkOut, 4000))))
	}
	slog.Info("verify stage done", "phase", "run_checkpoints", "verifyTaskID", cfg.VerifyTaskID, "duration", time.Since(stageStart), "passed", report.Passed())
	if !report.Passed() {
		issues := make([]breakfixv1.VerifyIssue, 0)
		for _, check := range report.Checks {
			if check.Passed {
				continue
			}
			issues = append(issues, breakfixv1.VerifyIssue{Code: "CHECKPOINT_" + strings.ToUpper(strings.ReplaceAll(check.ID, "-", "_")), Message: truncateStr(check.Summary+": "+check.Details, 4000)})
		}
		return updateVerifyFailure(ctx, client, cfg.VerifyTaskNS, task, &breakfixv1.VerifyReport{
			BuildPassed:  true,
			AnswerPassed: true,
			Summary:      "one or more checkpoints did not pass after answer.sh",
			Issues:       issues,
		})
	}

	err = mutateVerifyTaskStatus(ctx, client, cfg.VerifyTaskNS, task.Name, func(current *breakfixv1.VerifyTask) {
		current.Status.Phase = breakfixv1.VerifyTaskSucceeded
		current.Status.Message = "verification passed"
		current.Status.Report = &breakfixv1.VerifyReport{
			BuildPassed:       true,
			AnswerPassed:      true,
			CheckpointsPassed: true,
			Summary:           "all checkpoints passed",
		}
		done := metav1.Now()
		current.Status.CompletedAt = &done
	})
	if err == nil {
		slog.Info("verify task done", "verifyTaskID", cfg.VerifyTaskID, "submissionID", cfg.SubmissionID, "duration", time.Since(verifyStart))
	}
	return err
}

type verifyEnvironmentRef struct {
	Runtime string
	Name    string
}

type verifyEnvironmentState struct {
	Namespace string
	PodName   string
	Phase     breakfixv1.EnvironmentPhase
	Message   string
}

func verifyBaseImage(targetImage, runtime string) string {
	repoPrefix := ""
	if idx := strings.LastIndex(targetImage, "/"); idx >= 0 {
		repoPrefix = targetImage[:idx]
	}
	baseName := "breakfix-base:latest"
	if challenge.NormalizeRuntime(runtime) == challenge.RuntimeVCluster {
		baseName = "breakfix-k8s-base:latest"
	}
	if repoPrefix == "" {
		return baseName
	}
	return repoPrefix + "/" + baseName
}

func createVerifyEnvironment(ctx context.Context, client *k8s.Client, ns, verifyTaskID, submissionID string, entry *challenge.Entry, image string) (*verifyEnvironmentRef, error) {
	ref := &verifyEnvironmentRef{Runtime: challenge.NormalizeRuntime(entry.Runtime), Name: verification.EnvironmentName(verifyTaskID)}
	if err := deleteVerifyEnvironment(ctx, client, ns, ref); err != nil {
		return nil, fmt.Errorf("delete previous verification environment: %w", err)
	}
	if err := waitVerifyEnvironmentDeleted(ctx, client, ns, ref, 2*time.Minute); err != nil {
		return nil, err
	}

	checkpointIDs := make([]string, 0, len(entry.Checkpoints))
	for _, checkpoint := range entry.Checkpoints {
		checkpointIDs = append(checkpointIDs, checkpoint.ID)
	}
	activityAt := metav1.Now()
	common := breakfixv1.CommonEnvironmentSpec{
		ChallengeRef:      "verify-" + verifyTaskID,
		ChallengeRevision: "submission:" + submissionID,
		UserRef:           "verify-" + verifyTaskID,
		Runtime:           challenge.NormalizeRuntime(entry.Runtime),
		Image:             image,
		CheckpointIDs:     checkpointIDs,
		ActivityAt:        &activityAt,
	}

	labels := map[string]string{
		"breakfix.dev/verify-task": verifyTaskID,
		"breakfix.dev/submission":  submissionID,
	}

	if challenge.NormalizeRuntime(entry.Runtime) == challenge.RuntimeVCluster {
		_, err := client.CreateVClusterEnvironment(ctx, ns, &breakfixv1.VClusterEnvironment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      ref.Name,
				Namespace: ns,
				Labels:    labels,
			},
			Spec: breakfixv1.VClusterEnvironmentSpec{CommonEnvironmentSpec: common},
		})
		if err != nil {
			return nil, err
		}
		return ref, nil
	}

	_, err := client.CreateContainerEnvironment(ctx, ns, &breakfixv1.ContainerEnvironment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ref.Name,
			Namespace: ns,
			Labels:    labels,
		},
		Spec: common,
	})
	if err != nil {
		return nil, err
	}
	return ref, nil
}

func waitVerifyEnvironmentReady(ctx context.Context, client *k8s.Client, ns string, envRef *verifyEnvironmentRef, timeout time.Duration) (*verifyEnvironmentState, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		env, err := getVerifyEnvironment(ctx, client, ns, envRef)
		if err != nil {
			return nil, err
		}
		if env.Phase == breakfixv1.EnvironmentReady {
			return env, nil
		}
		if env.Phase == breakfixv1.EnvironmentDestroyed || env.Phase == breakfixv1.EnvironmentFailed {
			if strings.TrimSpace(env.Message) != "" {
				return nil, errors.New(env.Message)
			}
			return nil, fmt.Errorf("environment %s became unavailable before ready", envRef.Name)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return nil, fmt.Errorf("timeout waiting for verify environment %s ready", envRef.Name)
}

func getVerifyEnvironment(ctx context.Context, client *k8s.Client, ns string, envRef *verifyEnvironmentRef) (*verifyEnvironmentState, error) {
	switch challenge.NormalizeRuntime(envRef.Runtime) {
	case challenge.RuntimeVCluster:
		env, err := client.GetVClusterEnvironment(ctx, ns, envRef.Name)
		if err != nil {
			return nil, err
		}
		return &verifyEnvironmentState{
			Namespace: env.Status.Namespace,
			PodName:   env.Status.WorkspacePodName,
			Phase:     env.Status.Phase,
			Message:   env.Status.Message,
		}, nil
	default:
		env, err := client.GetContainerEnvironment(ctx, ns, envRef.Name)
		if err != nil {
			return nil, err
		}
		return &verifyEnvironmentState{
			Namespace: env.Status.Namespace,
			PodName:   env.Status.WorkspacePodName,
			Phase:     env.Status.Phase,
			Message:   env.Status.Message,
		}, nil
	}
}

func deleteVerifyEnvironment(ctx context.Context, client *k8s.Client, ns string, envRef *verifyEnvironmentRef) error {
	switch challenge.NormalizeRuntime(envRef.Runtime) {
	case challenge.RuntimeVCluster:
		err := client.DeleteVClusterEnvironment(ctx, ns, envRef.Name)
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	default:
		err := client.DeleteContainerEnvironment(ctx, ns, envRef.Name)
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
}

func waitVerifyEnvironmentDeleted(ctx context.Context, client *k8s.Client, ns string, envRef *verifyEnvironmentRef, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		_, err := getVerifyEnvironment(ctx, client, ns, envRef)
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return fmt.Errorf("timeout waiting for previous verification environment %s deletion", envRef.Name)
}

func downloadSubmission(ctx context.Context, cfg Config, dst string) error {
	url := strings.TrimRight(cfg.ServerURL, "/") + "/api/internal/verify-submissions/" + cfg.SubmissionID + "/artifact"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Breakfix-Internal-Key", cfg.InternalAPIKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("download submission: status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}

func updateVerifyFailure(ctx context.Context, client *k8s.Client, ns string, task *breakfixv1.VerifyTask, report *breakfixv1.VerifyReport) error {
	if report == nil {
		return fmt.Errorf("artifact verification failure has no report")
	}
	report.Class = breakfixv1.VerifyFailureArtifact
	return mutateVerifyTaskStatus(ctx, client, ns, task.Name, func(current *breakfixv1.VerifyTask) {
		current.Status.Phase = breakfixv1.VerifyTaskFailed
		current.Status.Report = report
		current.Status.Message = report.Summary
		done := metav1.Now()
		current.Status.CompletedAt = &done
	})
}

func failureReport(code, msg string) *breakfixv1.VerifyReport {
	return &breakfixv1.VerifyReport{
		Class:   breakfixv1.VerifyFailureArtifact,
		Summary: msg,
		Issues: []breakfixv1.VerifyIssue{{
			Code:    code,
			Message: msg,
		}},
	}
}

func mutateVerifyTaskStatus(ctx context.Context, client *k8s.Client, ns, name string, mutate func(*breakfixv1.VerifyTask)) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current, err := client.GetVerifyTask(ctx, ns, name)
		if err != nil {
			return err
		}
		if current.Status.Phase == breakfixv1.VerifyTaskSucceeded || current.Status.Phase == breakfixv1.VerifyTaskFailed {
			return nil
		}
		mutate(current)
		_, err = client.UpdateVerifyTaskStatus(ctx, ns, current)
		return err
	})
}

func truncateStr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
