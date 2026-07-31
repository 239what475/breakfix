package server

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/breakfix/breakfix/internal/agentruntime"
	"github.com/breakfix/breakfix/internal/api"
	"github.com/breakfix/breakfix/internal/taxonomy"
	"github.com/gin-gonic/gin"
)

type internalTaxonomyMapperFinalizeRequest struct {
	taxonomy.LeaseCredential
	ChangeSet taxonomy.ChangeSet `json:"changeset"`
}

type internalTaxonomyReviewFinalizeRequest struct {
	taxonomy.LeaseCredential
	Curriculum taxonomy.Review `json:"curriculum"`
	SRE        taxonomy.Review `json:"sre"`
}

// InternalTaxonomyContext exposes the Server-reconstructed model input for a
// single leased Taxonomy Run. The Worker receives no challenge directory,
// taxonomy filesystem path, or domain-table access.
func (h *Handler) InternalTaxonomyContext(c *gin.Context) {
	var credential taxonomy.LeaseCredential
	if !h.decodeInternalAgentRequest(c, &credential) {
		return
	}
	claim, err := h.internalTaxonomyClaim(c.Request.Context(), c.Param("id"), credential)
	if err != nil {
		h.writeInternalTaxonomyError(c, err)
		return
	}
	result, err := h.taxonomyWorkflow.LoadExecutionContext(c.Request.Context(), *claim)
	if err != nil {
		h.writeInternalTaxonomyError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

// InternalTaxonomyFinalizeMapper atomically promotes a valid mapper candidate
// and completes the generic Run through the Server-owned taxonomy repository.
func (h *Handler) InternalTaxonomyFinalizeMapper(c *gin.Context) {
	var request internalTaxonomyMapperFinalizeRequest
	if !h.decodeInternalAgentRequest(c, &request) {
		return
	}
	claim, err := h.internalTaxonomyClaim(c.Request.Context(), c.Param("id"), request.LeaseCredential)
	if err != nil {
		h.writeInternalTaxonomyError(c, err)
		return
	}
	if err := h.taxonomyWorkflow.FinalizeMapper(c.Request.Context(), *claim, request.ChangeSet); err != nil {
		h.writeInternalTaxonomyError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// InternalTaxonomyFinalizeReviewPair only commits the pair once both Eino
// reviewer invocations returned legal typed results in the same Worker Run.
func (h *Handler) InternalTaxonomyFinalizeReviewPair(c *gin.Context) {
	var request internalTaxonomyReviewFinalizeRequest
	if !h.decodeInternalAgentRequest(c, &request) {
		return
	}
	claim, err := h.internalTaxonomyClaim(c.Request.Context(), c.Param("id"), request.LeaseCredential)
	if err != nil {
		h.writeInternalTaxonomyError(c, err)
		return
	}
	if err := h.taxonomyWorkflow.FinalizeReviewPair(c.Request.Context(), *claim, request.Curriculum, request.SRE); err != nil {
		h.writeInternalTaxonomyError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) internalTaxonomyClaim(ctx context.Context, runID string, credential taxonomy.LeaseCredential) (*agentruntime.Claim, error) {
	if h.db == nil || h.taxonomyWorkflow == nil {
		return nil, errors.New("taxonomy runtime is unavailable")
	}
	if strings.TrimSpace(runID) == "" || !credential.Valid() {
		return nil, errors.New("taxonomy run lease credentials are required")
	}
	claim, err := h.getAgentClaim(ctx, runID, credential)
	if err != nil {
		return nil, err
	}
	run := &claim.Run
	input, err := taxonomy.DecodeRunInput(run.Input)
	if err != nil {
		return nil, err
	}
	purpose, err := taxonomy.PurposeForStage(input.Stage)
	if err != nil {
		return nil, err
	}
	if run.Purpose != purpose || run.OwnerKind != "taxonomy-mapping" || run.OwnerRef != input.WorkID || run.SessionID != "" {
		return nil, errors.New("agent run is not a current taxonomy attempt")
	}
	return claim, nil
}

func (h *Handler) writeInternalTaxonomyError(c *gin.Context, err error) {
	status := http.StatusBadRequest
	switch {
	case errors.Is(err, agentruntime.ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, agentruntime.ErrLeaseLost), errors.Is(err, taxonomy.ErrNoCurrentRevision):
		status = http.StatusConflict
	}
	c.JSON(status, api.ErrorResponse{Error: err.Error()})
}
