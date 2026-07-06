package controller

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	breakfixv1 "github.com/breakfix/breakfix/apis/breakfix/v1"
	"github.com/breakfix/breakfix/internal/challenge"
	"github.com/breakfix/breakfix/internal/k8s"
	"gopkg.in/yaml.v3"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type VerifyTaskReconciler struct {
	client.Client
	K8s              *k8s.Client
	RegistryAddr     string
	RegistryInsecure bool
	CRDNamespace     string
	ChallengesDir    string
	DataDir          string
	InternalAPIKey   string
	ServerHost       string
	ServerPort       int
}

func (r *VerifyTaskReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var task breakfixv1.VerifyTask
	if err := r.Get(ctx, req.NamespacedName, &task); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	switch task.Status.Phase {
	case "", breakfixv1.VerifyTaskPending:
		return r.createVerifyJob(ctx, &task)
	case breakfixv1.VerifyTaskRunning:
		return r.trackVerifyJob(ctx, &task)
	case breakfixv1.VerifyTaskVerified:
		return r.publish(ctx, &task)
	case breakfixv1.VerifyTaskSucceeded:
		return ctrl.Result{}, nil
	case breakfixv1.VerifyTaskFailed:
		return r.syncGenerationFailure(ctx, &task)
	default:
		return ctrl.Result{}, nil
	}
}

func (r *VerifyTaskReconciler) createVerifyJob(ctx context.Context, task *breakfixv1.VerifyTask) (ctrl.Result, error) {
	jobName := "verify-" + k8s.RandomID()
	env := map[string]string{
		"BREAKFIX_MODE":            "verify",
		"VERIFY_TASK_ID":           task.Name,
		"VERIFY_TASK_NAMESPACE":    r.CRDNamespace,
		"VERIFY_SUBMISSION_ID":     task.Spec.Submission.ID,
		"GATEWAY_INTERNAL_URL":     r.internalGatewayURL(),
		"GATEWAY_INTERNAL_API_KEY": r.InternalAPIKey,
		"REGISTRY_ADDR":            r.RegistryAddr,
		"LAB_NAMESPACE":            r.CRDNamespace,
	}
	if r.RegistryInsecure {
		env["REGISTRY_INSECURE"] = "true"
	}

	if err := r.K8s.CreateJob(r.CRDNamespace, jobName, k8s.CreateJobOpts{
		Image:           generatorImage(r.RegistryAddr),
		Env:             env,
		ImagePullPolicy: corev1.PullAlways,
	}); err != nil {
		task.Status.Phase = breakfixv1.VerifyTaskFailed
		task.Status.Message = fmt.Sprintf("create verify job: %v", err)
		now := metav1.Now()
		task.Status.CompletedAt = &now
		_, _ = r.K8s.UpdateVerifyTaskStatus(ctx, r.CRDNamespace, task)
		return ctrl.Result{}, err
	}

	task.Status.Phase = breakfixv1.VerifyTaskRunning
	task.Status.JobName = jobName
	task.Status.Message = "verify job created"
	now := metav1.Now()
	task.Status.StartedAt = &now
	if _, err := r.K8s.UpdateVerifyTaskStatus(ctx, r.CRDNamespace, task); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

func (r *VerifyTaskReconciler) trackVerifyJob(ctx context.Context, task *breakfixv1.VerifyTask) (ctrl.Result, error) {
	job, err := r.K8s.Clientset().BatchV1().Jobs(r.CRDNamespace).Get(ctx, task.Status.JobName, metav1.GetOptions{})
	if err != nil {
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	for _, c := range job.Status.Conditions {
		switch c.Type {
		case batchv1.JobComplete:
			refreshed, err := r.K8s.GetVerifyTask(ctx, r.CRDNamespace, task.Name)
			if err != nil {
				return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
			}
			if refreshed.Status.Phase == breakfixv1.VerifyTaskRunning {
				refreshed.Status.Phase = breakfixv1.VerifyTaskFailed
				refreshed.Status.Message = "verify job finished without reporting result"
				now := metav1.Now()
				refreshed.Status.CompletedAt = &now
				refreshed.Status.Report = &breakfixv1.VerifyReport{
					Summary: "verify job finished without reporting result",
					Issues: []breakfixv1.VerifyIssue{{
						Code:    "VERIFY_RESULT_MISSING",
						Message: "verify job finished without reporting result",
					}},
				}
				if _, err := r.K8s.UpdateVerifyTaskStatus(ctx, r.CRDNamespace, refreshed); err != nil {
					return ctrl.Result{}, err
				}
			}
			return ctrl.Result{}, nil
		case batchv1.JobFailed:
			task.Status.Phase = breakfixv1.VerifyTaskFailed
			task.Status.Message = fmt.Sprintf("verify job failed: %s", c.Message)
			now := metav1.Now()
			task.Status.CompletedAt = &now
			task.Status.Report = &breakfixv1.VerifyReport{
				Summary: task.Status.Message,
				Issues: []breakfixv1.VerifyIssue{{
					Code:    "VERIFY_JOB_FAILED",
					Message: task.Status.Message,
				}},
			}
			if _, err := r.K8s.UpdateVerifyTaskStatus(ctx, r.CRDNamespace, task); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, nil
		}
	}

	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

func (r *VerifyTaskReconciler) publish(ctx context.Context, task *breakfixv1.VerifyTask) (ctrl.Result, error) {
	if strings.TrimSpace(task.Annotations["breakfix.dev/published"]) == "true" {
		if task.Status.Phase != breakfixv1.VerifyTaskSucceeded {
			task.Status.Phase = breakfixv1.VerifyTaskSucceeded
			if strings.TrimSpace(task.Status.Message) == "" || task.Status.Message == "verification passed, waiting for publish" {
				task.Status.Message = "verification passed and published"
			}
			if task.Status.CompletedAt == nil {
				done := metav1.Now()
				task.Status.CompletedAt = &done
			}
			if _, err := r.K8s.UpdateVerifyTaskStatus(ctx, r.CRDNamespace, task); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}
	if task.Spec.Source.Kind == "agent" && strings.TrimSpace(task.Spec.Source.Ref) != "" {
		if gen, err := r.K8s.GetGeneration(ctx, r.CRDNamespace, task.Spec.Source.Ref); err == nil {
			if gen.Status.Phase == breakfixv1.GenerationSucceeded && gen.Status.Challenge != nil {
				return ctrl.Result{}, nil
			}
		}
	}
	ch, err := challenge.Get(r.ChallengesDir, task.Spec.ChallengeID)
	if err != nil {
		ch, err = r.materializeSubmission(task.Spec.ChallengeID, task.Spec.Submission.ID, task.Status.TempImage)
	}
	if err != nil {
		if err := r.syncGenerationPublishFailure(ctx, task, err.Error()); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, err
	}
	if err := r.syncGenerationSuccess(ctx, task, ch); err != nil {
		return ctrl.Result{}, err
	}
	if task.Annotations == nil {
		task.Annotations = map[string]string{}
	}
	task.Annotations["breakfix.dev/published"] = "true"
	if _, err := r.K8s.UpdateVerifyTask(ctx, r.CRDNamespace, task); err != nil {
		return ctrl.Result{}, err
	}
	task.Status.Phase = breakfixv1.VerifyTaskSucceeded
	task.Status.Message = fmt.Sprintf("verification passed and published as %s", ch.ID)
	done := metav1.Now()
	task.Status.CompletedAt = &done
	if _, err := r.K8s.UpdateVerifyTaskStatus(ctx, r.CRDNamespace, task); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *VerifyTaskReconciler) materializeSubmission(challengeID, submissionID, publishedImage string) (*challenge.Entry, error) {
	path := challenge.SubmissionPath(r.DataDir, submissionID)
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open submission artifact: %w", err)
	}
	defer f.Close()

	staging := filepath.Join(r.DataDir, ".publish-"+submissionID)
	_ = os.RemoveAll(staging)
	if err := os.MkdirAll(staging, 0755); err != nil {
		return nil, fmt.Errorf("create publish staging: %w", err)
	}
	defer os.RemoveAll(staging) //nolint:errcheck

	if err := challenge.ExtractTarGz(staging, f); err != nil {
		return nil, fmt.Errorf("extract submission artifact: %w", err)
	}
	_, err = challenge.LoadSubmissionDir(staging)
	if err != nil {
		return nil, err
	}
	if !challenge.ValidID(challengeID) {
		return nil, fmt.Errorf("invalid challenge id %q", challengeID)
	}

	manifestPath := filepath.Join(staging, "challenge.yaml")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("read challenge manifest: %w", err)
	}
	var manifest challenge.Spec
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("parse challenge manifest: %w", err)
	}
	manifest.ID = challengeID
	if strings.TrimSpace(publishedImage) == "" {
		return nil, fmt.Errorf("published image is empty")
	}
	manifest.Image = publishedImage
	if strings.TrimSpace(manifest.Type) == "" {
		manifest.Type = challenge.TypeScript
	}
	normalized, err := yaml.Marshal(manifest)
	if err != nil {
		return nil, fmt.Errorf("marshal challenge manifest: %w", err)
	}
	if err := os.WriteFile(manifestPath, normalized, 0644); err != nil {
		return nil, fmt.Errorf("write challenge manifest: %w", err)
	}

	return challenge.Materialize(r.ChallengesDir, challengeID, func(dst string) error {
		return copyDirForPublish(staging, dst)
	})
}

func (r *VerifyTaskReconciler) syncGenerationFailure(ctx context.Context, task *breakfixv1.VerifyTask) (ctrl.Result, error) {
	if task.Spec.Source.Kind != "agent" || strings.TrimSpace(task.Spec.Source.Ref) == "" {
		return ctrl.Result{}, nil
	}
	gen, err := r.K8s.GetGeneration(ctx, r.CRDNamespace, task.Spec.Source.Ref)
	if err != nil {
		return ctrl.Result{}, err
	}
	if gen.Status.Phase == breakfixv1.GenerationFailed {
		return ctrl.Result{}, nil
	}
	gen.Status.Phase = breakfixv1.GenerationFailed
	gen.Status.Message = generationFailureMessage(task)
	now := metav1.Now()
	gen.Status.CompletedAt = &now
	if _, err := r.K8s.UpdateGenerationStatus(ctx, r.CRDNamespace, gen); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *VerifyTaskReconciler) syncGenerationPublishFailure(ctx context.Context, task *breakfixv1.VerifyTask, msg string) error {
	if task.Spec.Source.Kind != "agent" || strings.TrimSpace(task.Spec.Source.Ref) == "" {
		return nil
	}
	gen, err := r.K8s.GetGeneration(ctx, r.CRDNamespace, task.Spec.Source.Ref)
	if err != nil {
		return err
	}
	gen.Status.Phase = breakfixv1.GenerationFailed
	gen.Status.Message = fmt.Sprintf("publish failed: %s", msg)
	now := metav1.Now()
	gen.Status.CompletedAt = &now
	_, err = r.K8s.UpdateGenerationStatus(ctx, r.CRDNamespace, gen)
	return err
}

func generationFailureMessage(task *breakfixv1.VerifyTask) string {
	if task == nil {
		return ""
	}
	base := strings.TrimSpace(task.Status.Message)
	report := task.Status.Report
	if report == nil {
		return base
	}

	var parts []string
	if summary := strings.TrimSpace(report.Summary); summary != "" && summary != base {
		parts = append(parts, summary)
	}
	for _, issue := range report.Issues {
		msg := strings.TrimSpace(issue.Message)
		if msg == "" {
			continue
		}
		if code := strings.TrimSpace(issue.Code); code != "" {
			msg = code + ": " + msg
		}
		parts = append(parts, msg)
		break
	}
	if len(parts) == 0 {
		return base
	}
	if base != "" {
		parts = append([]string{base}, parts...)
	}
	return truncateFailureText(strings.Join(parts, "\n\n"))
}

func truncateFailureText(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= 4000 {
		return s
	}
	return strings.TrimSpace(s[:4000]) + "..."
}

func (r *VerifyTaskReconciler) syncGenerationSuccess(ctx context.Context, task *breakfixv1.VerifyTask, ch *challenge.Entry) error {
	if task.Spec.Source.Kind != "agent" || strings.TrimSpace(task.Spec.Source.Ref) == "" {
		return nil
	}
	gen, err := r.K8s.GetGeneration(ctx, r.CRDNamespace, task.Spec.Source.Ref)
	if err != nil {
		return err
	}
	if gen.Status.Phase == breakfixv1.GenerationSucceeded && gen.Status.Challenge != nil {
		return nil
	}
	gen.Status.Phase = breakfixv1.GenerationSucceeded
	gen.Status.Message = fmt.Sprintf("challenge %s generated", ch.ID)
	gen.Status.Challenge = &breakfixv1.ChallengeSpec{
		ID:          ch.ID,
		Title:       ch.Title,
		Type:        ch.Type,
		Runtime:     ch.Runtime,
		Difficulty:  ch.Difficulty,
		Tags:        append([]string{}, ch.Tags...),
		Description: ch.Description,
		Image:       ch.Image,
	}
	now := metav1.Now()
	gen.Status.CompletedAt = &now
	_, err = r.K8s.UpdateGenerationStatus(ctx, r.CRDNamespace, gen)
	return err
}

func (r *VerifyTaskReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&breakfixv1.VerifyTask{}).
		Complete(r)
}

func (r *VerifyTaskReconciler) internalGatewayURL() string {
	host := strings.TrimSpace(r.ServerHost)
	if host == "" {
		host = "172.18.0.1"
	}
	return fmt.Sprintf("http://%s:%d", host, r.ServerPort)
}

func generatorImage(registryAddr string) string {
	if strings.TrimSpace(registryAddr) == "" {
		return "breakfix-generator:latest"
	}
	return registryAddr + "/breakfix-generator:latest"
}

func copyDirForPublish(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0755); err != nil {
		return err
	}
	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if err := os.MkdirAll(dstPath, info.Mode().Perm()); err != nil {
				return err
			}
			continue
		}
		data, err := os.ReadFile(srcPath)
		if err != nil {
			return err
		}
		if err := os.WriteFile(dstPath, data, info.Mode().Perm()); err != nil {
			return err
		}
	}
	return nil
}
