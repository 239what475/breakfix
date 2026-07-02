package generator

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

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

	if err := downloadSubmission(ctx, cfg, filepath.Join(workDir, "artifact.tar.gz")); err != nil {
		return updateVerifyFailure(ctx, client, cfg.VerifyTaskNS, task, failureReport("SUBMISSION_DOWNLOAD_FAILED", err.Error()))
	}
	f, err := os.Open(filepath.Join(workDir, "artifact.tar.gz"))
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

	tempImage := fmt.Sprintf("%s/verify-%s:latest", cfg.RegistryAddr, cfg.VerifyTaskID)
	if err := mutateVerifyTaskStatus(ctx, client, cfg.VerifyTaskNS, task.Name, func(current *breakfixv1.VerifyTask) {
		current.Status.TempImage = tempImage
		current.Status.Message = "building verification image"
	}); err != nil {
		return fmt.Errorf("update verify task status: %w", err)
	}
	if ok := BuildAndPush(ctx, tempImage, chalDir, cfg.RegistryInsecure); !ok {
		return updateVerifyFailure(ctx, client, cfg.VerifyTaskNS, task, &breakfixv1.VerifyReport{
			Summary: "image build or push failed",
			Issues: []breakfixv1.VerifyIssue{{
				Code:    "BUILD_FAILED",
				Message: "image build or push failed",
			}},
		})
	}

	podName := "verify-" + k8s.RandomID()
	if err := mutateVerifyTaskStatus(ctx, client, cfg.VerifyTaskNS, task.Name, func(current *breakfixv1.VerifyTask) {
		current.Status.PodName = podName
		current.Status.Message = "running answer.sh and verify.sh"
	}); err != nil {
		return fmt.Errorf("update verify task status: %w", err)
	}

	if err := client.EnsureNamespace(cfg.LabNS); err != nil {
		return updateVerifyFailure(ctx, client, cfg.VerifyTaskNS, task, failureReport("VERIFY_NAMESPACE_FAILED", err.Error()))
	}
	if err := client.CreatePod(cfg.LabNS, podName, k8s.CreatePodOpts{Image: tempImage}); err != nil {
		return updateVerifyFailure(ctx, client, cfg.VerifyTaskNS, task, failureReport("VERIFY_POD_CREATE_FAILED", err.Error()))
	}
	defer client.DeletePod(cfg.LabNS, podName) //nolint:errcheck

	if err := client.WaitForPod(cfg.LabNS, podName, "challenge"); err != nil {
		return updateVerifyFailure(ctx, client, cfg.VerifyTaskNS, task, failureReport("VERIFY_POD_NOT_READY", err.Error()))
	}

	answerPath := filepath.Join(chalDir, "answer.sh")
	if err := client.CopyToPod(cfg.LabNS, podName, answerPath, k8s.PodAnswerPath); err != nil {
		return updateVerifyFailure(ctx, client, cfg.VerifyTaskNS, task, failureReport("ANSWER_COPY_FAILED", err.Error()))
	}
	ansExit, ansOut, err := client.ExecInPod(cfg.LabNS, podName, "bash", k8s.PodAnswerPath)
	if err != nil {
		return updateVerifyFailure(ctx, client, cfg.VerifyTaskNS, task, failureReport("ANSWER_EXEC_FAILED", err.Error()))
	}
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

	verifyPath := filepath.Join(chalDir, "verify.sh")
	if err := client.CopyToPod(cfg.LabNS, podName, verifyPath, k8s.PodVerifyPath); err != nil {
		return updateVerifyFailure(ctx, client, cfg.VerifyTaskNS, task, failureReport("VERIFY_COPY_FAILED", err.Error()))
	}
	verifyExit, verifyOut, err := client.ExecInPod(cfg.LabNS, podName, "bash", k8s.PodVerifyPath)
	if err != nil {
		return updateVerifyFailure(ctx, client, cfg.VerifyTaskNS, task, failureReport("VERIFY_EXEC_FAILED", err.Error()))
	}
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

	return mutateVerifyTaskStatus(ctx, client, cfg.VerifyTaskNS, task.Name, func(current *breakfixv1.VerifyTask) {
		current.Status.Phase = breakfixv1.VerifyTaskVerified
		current.Status.Message = "verification passed, waiting for publish"
		current.Status.Report = &breakfixv1.VerifyReport{
			BuildPassed:  true,
			AnswerPassed: true,
			VerifyPassed: true,
			Summary:      "verification passed",
		}
	})
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
