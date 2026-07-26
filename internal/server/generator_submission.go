package server

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"

	"github.com/breakfix/breakfix/internal/challenge"
	"github.com/breakfix/breakfix/internal/generator"
	breakfixv1 "github.com/breakfix/breakfix/internal/k8s/apis/breakfix/v1"
	"github.com/breakfix/breakfix/internal/verification"
	"github.com/gin-gonic/gin"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// InternalGeneratorSubmitCandidate is the short, deterministic handoff after
// a Judge pass. It validates the exact archive, persists it once, and
// create-or-gets its VerifyTask before atomically completing the Generator Run.
func (h *Handler) InternalGeneratorSubmitCandidate(c *gin.Context) {
	var request internalGeneratorSubmitRequest
	if !h.decodeInternalAgentRequest(c, &request) {
		return
	}
	claim, _, err := h.generatorWorkspaceForClaim(c.Request.Context(), c.Param("id"), request.LeaseCredential, false)
	if err != nil {
		h.writeInternalGeneratorError(c, err)
		return
	}
	if len(request.Archive) == 0 {
		h.writeInternalGeneratorError(c, fmt.Errorf("generator candidate archive is required"))
		return
	}
	if err := validateGeneratorCandidateArchive(h.dataDir, request.Archive); err != nil {
		h.writeInternalGeneratorError(c, err)
		return
	}
	submissionID := generator.SubmissionID(claim.Run.ID)
	if _, err := challenge.SaveSubmissionAtomic(h.dataDir, submissionID, bytes.NewReader(request.Archive)); err != nil {
		h.writeInternalGeneratorError(c, fmt.Errorf("save generator candidate: %w", err))
		return
	}
	task, err := h.createOrGetGeneratorVerifyTask(c.Request.Context(), submissionID, claim.Run.ID)
	if err != nil {
		h.writeInternalGeneratorError(c, err)
		return
	}
	if err := h.db.FinalizeGeneratorSubmission(c.Request.Context(), *claim, submissionID, task.Name); err != nil {
		h.writeInternalGeneratorError(c, err)
		return
	}
	c.JSON(http.StatusOK, generator.Submission{ID: submissionID, VerifyTask: task.Name})
}

func validateGeneratorCandidateArchive(dataDir string, archive []byte) error {
	temporary, err := os.MkdirTemp(dataDir, ".generator-candidate-")
	if err != nil {
		return fmt.Errorf("create generator candidate staging: %w", err)
	}
	defer os.RemoveAll(temporary) //nolint:errcheck
	if err := challenge.ExtractTarGz(temporary, bytes.NewReader(archive)); err != nil {
		return fmt.Errorf("extract generator candidate: %w", err)
	}
	if _, err := challenge.ValidateSubmissionDir(temporary); err != nil {
		return fmt.Errorf("validate generator candidate: %w", err)
	}
	return nil
}

func (h *Handler) createOrGetGeneratorVerifyTask(ctx context.Context, submissionID, runID string) (*breakfixv1.VerifyTask, error) {
	if h.k8s == nil {
		return nil, fmt.Errorf("verify task runtime is unavailable")
	}
	name := verification.TaskName(submissionID)
	task := &breakfixv1.VerifyTask{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: h.crdNamespace},
		Spec: breakfixv1.VerifyTaskSpec{
			Source:     breakfixv1.VerifyTaskSource{Ref: runID},
			Submission: breakfixv1.VerifyTaskSubmission{ID: submissionID},
		},
	}
	created, err := h.k8s.CreateVerifyTask(ctx, h.crdNamespace, task)
	if err == nil {
		return created, nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return nil, fmt.Errorf("create generator verify task: %w", err)
	}
	existing, err := h.k8s.GetVerifyTask(ctx, h.crdNamespace, name)
	if err != nil {
		return nil, fmt.Errorf("get existing generator verify task: %w", err)
	}
	if existing.Spec.Source.Ref != runID || existing.Spec.Submission.ID != submissionID {
		return nil, fmt.Errorf("verify task %s does not match generator submission", name)
	}
	return existing, nil
}
