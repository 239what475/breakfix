package generator

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

	breakfixv1 "github.com/breakfix/breakfix/apis/breakfix/v1"
	"github.com/breakfix/breakfix/internal/challenge"
	"github.com/breakfix/breakfix/internal/k8s"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
)

type VerifyTaskConfig struct {
	Kubeconfig       string
	LabNS            string
	RegistryAddr     string
	RegistryInsecure bool
	GatewayURL       string
	InternalAPIKey   string
	VerifyTaskID     string
	VerifyTaskNS     string
	SubmissionID     string
}

func RunVerifyTask(ctx context.Context, cfg VerifyTaskConfig) error {
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

	workDir, err := os.MkdirTemp("", "breakfix-verify-*")
	if err != nil {
		return fmt.Errorf("create temp workdir: %w", err)
	}
	defer os.RemoveAll(workDir) //nolint:errcheck

	downloadPath := filepath.Join(workDir, "artifact.tar.gz")
	stageStart := time.Now()
	slog.Info("verify stage start", "phase", "download_submission", "verifyTaskID", cfg.VerifyTaskID)
	if err := downloadSubmission(ctx, cfg, downloadPath); err != nil {
		return updateVerifyFailure(ctx, client, cfg.VerifyTaskNS, task, failureReport("SUBMISSION_DOWNLOAD_FAILED", err.Error()))
	}
	slog.Info("verify stage done", "phase", "download_submission", "verifyTaskID", cfg.VerifyTaskID, "duration", time.Since(stageStart), "path", downloadPath)

	stageStart = time.Now()
	slog.Info("verify stage start", "phase", "extract_artifact", "verifyTaskID", cfg.VerifyTaskID)
	f, err := os.Open(downloadPath)
	if err != nil {
		return updateVerifyFailure(ctx, client, cfg.VerifyTaskNS, task, failureReport("SUBMISSION_OPEN_FAILED", err.Error()))
	}
	defer f.Close()

	chalDir := filepath.Join(workDir, "challenge")
	if err := os.MkdirAll(chalDir, 0755); err != nil {
		return updateVerifyFailure(ctx, client, cfg.VerifyTaskNS, task, failureReport("VERIFY_WORKDIR_CREATE_FAILED", err.Error()))
	}
	if err := challenge.ExtractTarGz(chalDir, f); err != nil {
		return updateVerifyFailure(ctx, client, cfg.VerifyTaskNS, task, failureReport("ARTIFACT_EXTRACT_FAILED", err.Error()))
	}
	if _, err := challenge.ValidateDir(chalDir); err != nil {
		return updateVerifyFailure(ctx, client, cfg.VerifyTaskNS, task, failureReport("STRUCTURE_INVALID", err.Error()))
	}
	challengeEntry, err := challenge.LoadDir(chalDir)
	if err != nil {
		return updateVerifyFailure(ctx, client, cfg.VerifyTaskNS, task, failureReport("CHALLENGE_MANIFEST_INVALID", err.Error()))
	}
	slog.Info("verify stage done", "phase", "extract_artifact", "verifyTaskID", cfg.VerifyTaskID, "duration", time.Since(stageStart), "challengeID", challengeEntry.ID, "runtime", challengeEntry.Runtime)

	tempImage := fmt.Sprintf("%s/verify-%s:latest", cfg.RegistryAddr, cfg.VerifyTaskID)
	if err := mutateVerifyTaskStatus(ctx, client, cfg.VerifyTaskNS, task.Name, func(current *breakfixv1.VerifyTask) {
		current.Status.TempImage = tempImage
		current.Status.Message = "building verification image"
	}); err != nil {
		return fmt.Errorf("update verify task status: %w", err)
	}
	stageStart = time.Now()
	slog.Info("verify stage start", "phase", "build_image", "verifyTaskID", cfg.VerifyTaskID, "image", tempImage)
	if ok := BuildAndPush(ctx, tempImage, chalDir, verifyBaseImage(tempImage, challengeEntry.Runtime), cfg.RegistryInsecure); !ok {
		return updateVerifyFailure(ctx, client, cfg.VerifyTaskNS, task, &breakfixv1.VerifyReport{
			Summary: "image build or push failed",
			Issues: []breakfixv1.VerifyIssue{{
				Code:    "BUILD_FAILED",
				Message: "image build or push failed",
			}},
		})
	}
	slog.Info("verify stage done", "phase", "build_image", "verifyTaskID", cfg.VerifyTaskID, "duration", time.Since(stageStart), "image", tempImage)

	stageStart = time.Now()
	slog.Info("verify stage start", "phase", "create_environment", "verifyTaskID", cfg.VerifyTaskID, "runtime", challengeEntry.Runtime)
	envRef, err := createVerifyEnvironment(ctx, client, cfg.VerifyTaskNS, cfg.VerifyTaskID, cfg.SubmissionID, challengeEntry, tempImage)
	if err != nil {
		return updateVerifyFailure(ctx, client, cfg.VerifyTaskNS, task, failureReport("VERIFY_ENV_CREATE_FAILED", err.Error()))
	}
	defer destroyVerifyEnvironment(context.Background(), client, cfg.VerifyTaskNS, envRef) //nolint:errcheck
	slog.Info("verify stage done", "phase", "create_environment", "verifyTaskID", cfg.VerifyTaskID, "duration", time.Since(stageStart), "environment", envRef.Name, "runtime", envRef.Runtime)

	if err := mutateVerifyTaskStatus(ctx, client, cfg.VerifyTaskNS, task.Name, func(current *breakfixv1.VerifyTask) {
		current.Status.Message = "starting verification environment"
	}); err != nil {
		return fmt.Errorf("update verify task status: %w", err)
	}

	stageStart = time.Now()
	slog.Info("verify stage start", "phase", "wait_environment_ready", "verifyTaskID", cfg.VerifyTaskID, "environment", envRef.Name)
	readyEnv, err := waitVerifyEnvironmentReady(ctx, client, cfg.VerifyTaskNS, envRef, 15*time.Minute)
	if err != nil {
		return updateVerifyFailure(ctx, client, cfg.VerifyTaskNS, task, &breakfixv1.VerifyReport{
			BuildPassed: true,
			Summary:     "verification environment failed before ready",
			Issues: []breakfixv1.VerifyIssue{{
				Code:    "VERIFY_ENV_NOT_READY",
				Message: truncateStr(err.Error(), 4000),
			}},
		})
	}
	slog.Info("verify stage done", "phase", "wait_environment_ready", "verifyTaskID", cfg.VerifyTaskID, "duration", time.Since(stageStart), "pod", readyEnv.PodName, "namespace", readyEnv.Namespace)

	if err := mutateVerifyTaskStatus(ctx, client, cfg.VerifyTaskNS, task.Name, func(current *breakfixv1.VerifyTask) {
		current.Status.PodName = readyEnv.PodName
		current.Status.Message = "running answer.sh and verify.sh"
	}); err != nil {
		return fmt.Errorf("update verify task status: %w", err)
	}

	stageStart = time.Now()
	slog.Info("verify stage start", "phase", "run_answer", "verifyTaskID", cfg.VerifyTaskID, "pod", readyEnv.PodName)
	ansExit, ansOut, err := client.ExecInPod(readyEnv.Namespace, readyEnv.PodName, "bash", "/answer.sh")
	if err != nil {
		return updateVerifyFailure(ctx, client, cfg.VerifyTaskNS, task, failureReport("ANSWER_EXEC_FAILED", err.Error()))
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
	slog.Info("verify stage start", "phase", "run_verify", "verifyTaskID", cfg.VerifyTaskID, "pod", readyEnv.PodName)
	verifyExit, verifyOut, err := client.ExecInPod(readyEnv.Namespace, readyEnv.PodName, "bash", "/verify.sh")
	if err != nil {
		return updateVerifyFailure(ctx, client, cfg.VerifyTaskNS, task, failureReport("VERIFY_EXEC_FAILED", err.Error()))
	}
	slog.Info("verify stage done", "phase", "run_verify", "verifyTaskID", cfg.VerifyTaskID, "duration", time.Since(stageStart), "exitCode", verifyExit)
	if verifyExit != 0 {
		return updateVerifyFailure(ctx, client, cfg.VerifyTaskNS, task, &breakfixv1.VerifyReport{
			BuildPassed:  true,
			AnswerPassed: true,
			Summary:      fmt.Sprintf("verify.sh exit=%d", verifyExit),
			Issues: []breakfixv1.VerifyIssue{{
				Code:    "VERIFY_EXIT_NONZERO",
				Message: truncateStr(verifyOut, 4000),
			}},
		})
	}

	err = mutateVerifyTaskStatus(ctx, client, cfg.VerifyTaskNS, task.Name, func(current *breakfixv1.VerifyTask) {
		current.Status.Phase = breakfixv1.VerifyTaskVerified
		current.Status.Message = "verification passed, waiting for publish"
		current.Status.Report = &breakfixv1.VerifyReport{
			BuildPassed:  true,
			AnswerPassed: true,
			VerifyPassed: true,
			Summary:      "verification passed",
		}
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
	if strings.TrimSpace(runtime) == "vcluster" {
		baseName = "breakfix-k8s-base:latest"
	}
	if repoPrefix == "" {
		return baseName
	}
	return repoPrefix + "/" + baseName
}

func createVerifyEnvironment(ctx context.Context, client *k8s.Client, ns, verifyTaskID, submissionID string, entry *challenge.Entry, image string) (*verifyEnvironmentRef, error) {
	name := "verify-" + k8s.RandomID()
	common := breakfixv1.CommonEnvironmentSpec{
		ChallengeRef: entry.ID,
		UserRef:      "verify-" + verifyTaskID,
		Image:        image,
	}

	labels := map[string]string{
		"breakfix.dev/verify-task": verifyTaskID,
		"breakfix.dev/submission":  submissionID,
	}

	if strings.TrimSpace(entry.Runtime) == "vcluster" {
		_, err := client.CreateVClusterEnvironment(ctx, ns, &breakfixv1.VClusterEnvironment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: ns,
				Labels:    labels,
			},
			Spec: breakfixv1.VClusterEnvironmentSpec{CommonEnvironmentSpec: common},
		})
		if err != nil {
			return nil, err
		}
		return &verifyEnvironmentRef{Runtime: "vcluster", Name: name}, nil
	}

	_, err := client.CreateContainerEnvironment(ctx, ns, &breakfixv1.ContainerEnvironment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels:    labels,
		},
		Spec: common,
	})
	if err != nil {
		return nil, err
	}
	return &verifyEnvironmentRef{Runtime: "container", Name: name}, nil
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
	switch envRef.Runtime {
	case "vcluster":
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

func destroyVerifyEnvironment(ctx context.Context, client *k8s.Client, ns string, envRef *verifyEnvironmentRef) error {
	switch envRef.Runtime {
	case "vcluster":
		env, err := client.GetVClusterEnvironment(ctx, ns, envRef.Name)
		if err != nil {
			return nil
		}
		env.Status.Phase = breakfixv1.EnvironmentDestroyed
		if _, err := client.UpdateVClusterEnvironmentStatus(ctx, ns, env); err != nil {
			return err
		}
		return client.DeleteVClusterEnvironment(ctx, ns, envRef.Name)
	default:
		env, err := client.GetContainerEnvironment(ctx, ns, envRef.Name)
		if err != nil {
			return nil
		}
		env.Status.Phase = breakfixv1.EnvironmentDestroyed
		if _, err := client.UpdateContainerEnvironmentStatus(ctx, ns, env); err != nil {
			return err
		}
		return client.DeleteContainerEnvironment(ctx, ns, envRef.Name)
	}
}

func downloadSubmission(ctx context.Context, cfg VerifyTaskConfig, dst string) error {
	url := strings.TrimRight(cfg.GatewayURL, "/") + "/api/internal/verify-submissions/" + cfg.SubmissionID + "/artifact"
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
		mutate(current)
		_, err = client.UpdateVerifyTaskStatus(ctx, ns, current)
		return err
	})
}
