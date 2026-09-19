package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/breakfix/breakfix/internal/domain/audit"
	documentdomain "github.com/breakfix/breakfix/internal/domain/documentpractice"
	api "github.com/breakfix/breakfix/internal/transport/httpapi/generated"
	"github.com/gin-gonic/gin"
)

// documentationBatchApplication creates declarative batches with their
// creation audit and drives the audited batch operations. Reads go through
// the repositories directly.
type documentationBatchApplication interface {
	CreateBatch(ctx context.Context, scope documentdomain.BatchScope, concurrency int, action *audit.HumanAction) (documentdomain.DocumentBatch, error)
	PauseBatch(ctx context.Context, batchID, actorID, reason string) (documentdomain.DocumentBatch, documentdomain.BatchItemCounts, error)
	ResumeBatch(ctx context.Context, batchID, actorID, reason string) (documentdomain.DocumentBatch, documentdomain.BatchItemCounts, error)
	CancelBatch(ctx context.Context, batchID, actorID, reason string) (documentdomain.DocumentBatch, documentdomain.BatchItemCounts, error)
	RetryFailedItems(ctx context.Context, batchID, actorID, reason string) (documentdomain.DocumentBatch, documentdomain.BatchItemCounts, int, error)
}

const adminBatchInitialLimit = 20
const adminBatchItemInitialLimit = 50

// CreateAdminDocumentationBatch resolves the declared scope into deterministic
// items and accepts the batch. The batch stays Pending: the scheduler, not the
// caller, drives every later state.
func (h *Handler) CreateAdminDocumentationBatch(c *gin.Context) {
	if !h.requireDocumentationProduct(c) {
		return
	}
	if h.documentationBatches == nil {
		c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "documentation batches are not configured"})
		return
	}
	var request api.DocumentationBatchCreateRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: "scope is required"})
		return
	}
	scope, err := batchScopeFromAPI(request.Scope)
	if err != nil {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: err.Error()})
		return
	}
	actorID, _ := c.Get("user_id")
	actor, _ := actorID.(string)
	now := time.Now().UTC()
	action := audit.HumanAction{
		ID:         audit.NewID(now),
		UserID:     actor,
		Action:     audit.ActionDocumentationBatchCreate,
		TargetType: audit.TargetDocumentBatch,
		TargetID:   "pending",
		Detail:     []byte(`{}`),
		CreatedAt:  now,
	}
	batch, err := h.documentationBatches.CreateBatch(c.Request.Context(), scope, intValue(request.Concurrency), &action)
	if err != nil {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: err.Error()})
		return
	}
	created, counts, err := h.db.DocumentPractice.GetBatch(c.Request.Context(), batch.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: err.Error()})
		return
	}
	c.JSON(http.StatusAccepted, adminDocumentBatch(created, counts))
}

// ListAdminDocumentationBatches returns the newest batches with counts.
func (h *Handler) ListAdminDocumentationBatches(c *gin.Context, params api.ListAdminDocumentationBatchesParams) {
	if !h.requireDocumentationProduct(c) {
		return
	}
	limit := adminBatchInitialLimit
	if params.Limit != nil {
		limit = *params.Limit
	}
	if limit < 1 || limit > 100 {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: "batch list limit must be between 1 and 100"})
		return
	}
	batches, counts, err := h.db.DocumentPractice.ListBatches(c.Request.Context(), limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: err.Error()})
		return
	}
	page := api.AdminDocumentBatchList{Batches: make([]api.AdminDocumentBatch, 0, len(batches))}
	for index, batch := range batches {
		page.Batches = append(page.Batches, adminDocumentBatch(batch, counts[index]))
	}
	c.JSON(http.StatusOK, page)
}

// GetAdminDocumentationBatch returns one batch with counts.
func (h *Handler) GetAdminDocumentationBatch(c *gin.Context, batchID string) {
	if !h.requireDocumentationProduct(c) {
		return
	}
	batch, counts, err := h.db.DocumentPractice.GetBatch(c.Request.Context(), batchID)
	if errors.Is(err, documentdomain.ErrWorkflowNotFound) {
		c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "document batch not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: err.Error()})
		return
	}
	c.JSON(http.StatusOK, adminDocumentBatch(batch, counts))
}

// ListAdminDocumentationBatchItems pages one batch's items in corpus order.
func (h *Handler) ListAdminDocumentationBatchItems(c *gin.Context, batchID string, params api.ListAdminDocumentationBatchItemsParams) {
	if !h.requireDocumentationProduct(c) {
		return
	}
	if _, _, err := h.db.DocumentPractice.GetBatch(c.Request.Context(), batchID); err != nil {
		if errors.Is(err, documentdomain.ErrWorkflowNotFound) {
			c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "document batch not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: err.Error()})
		return
	}
	limit := adminBatchItemInitialLimit
	if params.Limit != nil {
		limit = *params.Limit
	}
	if limit < 1 || limit > 200 {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: "batch item limit must be between 1 and 200"})
		return
	}
	cursor, err := parseBatchItemCursor(params.Cursor)
	if err != nil {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: err.Error()})
		return
	}
	filter := documentdomain.BatchItemFilter{
		BatchID: batchID,
		Cursor:  cursor,
		Limit:   limit,
	}
	if params.State != nil {
		state := documentdomain.BatchItemState(*params.State)
		if !state.Valid() {
			c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: "batch item state filter is invalid"})
			return
		}
		filter.State = state
	}
	items, next, err := h.db.DocumentPractice.ListBatchItems(c.Request.Context(), filter)
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: err.Error()})
		return
	}
	page := api.AdminDocumentBatchItemsPage{Items: make([]api.AdminDocumentBatchItem, 0, len(items))}
	if next != nil {
		page.NextCursor = encodeBatchItemCursor(next.Ordinal)
	}
	for _, item := range items {
		page.Items = append(page.Items, adminDocumentBatchItem(item))
	}
	c.JSON(http.StatusOK, page)
}

func adminDocumentBatch(batch documentdomain.DocumentBatch, counts documentdomain.BatchItemCounts) api.AdminDocumentBatch {
	result := api.AdminDocumentBatch{
		Id:          batch.ID,
		State:       api.AdminDocumentBatchState(batch.State),
		Scope:       batchScopeToAPI(batch.Scope),
		Concurrency: batch.Concurrency,
		TotalItems:  batch.TotalItems,
		CreatedBy:   batch.CreatedBy,
		CreatedAt:   batch.CreatedAt,
		UpdatedAt:   batch.UpdatedAt,
	}
	resolution := api.AdminDocumentBatchResolution{ResolvedPages: batch.Resolution.Resolved}
	if len(batch.Resolution.Excluded) > 0 {
		excluded := make([]struct {
			PagePath string                                         `json:"page_path"`
			Reason   api.AdminDocumentBatchResolutionExcludedReason `json:"reason"`
		}, 0, len(batch.Resolution.Excluded))
		for _, entry := range batch.Resolution.Excluded {
			excluded = append(excluded, struct {
				PagePath string                                         `json:"page_path"`
				Reason   api.AdminDocumentBatchResolutionExcludedReason `json:"reason"`
			}{PagePath: entry.PagePath, Reason: api.AdminDocumentBatchResolutionExcludedReason(entry.Reason)})
		}
		resolution.Excluded = &excluded
	}
	result.Resolution = resolution
	if len(counts) > 0 {
		encoded := map[string]int{}
		for state, count := range counts {
			encoded[string(state)] = count
		}
		result.Counts = &encoded
	}
	return result
}

func adminDocumentBatchItem(item documentdomain.BatchItem) api.AdminDocumentBatchItem {
	result := api.AdminDocumentBatchItem{
		Id:         item.ID,
		BatchId:    item.BatchID,
		Ordinal:    item.Ordinal,
		PagePath:   item.PagePath,
		Anchor:     item.Anchor,
		Title:      item.Title,
		WorkflowId: item.WorkflowID,
		State:      api.AdminDocumentBatchItemState(item.State),
		CreatedAt:  item.CreatedAt,
		UpdatedAt:  item.UpdatedAt,
	}
	if item.Detail != "" {
		result.Detail = &item.Detail
	}
	return result
}

func batchScopeFromAPI(scope api.AdminDocumentBatchScope) (documentdomain.BatchScope, error) {
	result := documentdomain.BatchScope{Kind: documentdomain.BatchScopeKind(scope.Kind)}
	if !result.Kind.Valid() {
		return documentdomain.BatchScope{}, fmt.Errorf("batch scope kind %q is unsupported", scope.Kind)
	}
	if scope.Sections != nil {
		result.Sections = append(result.Sections, *scope.Sections...)
	}
	if scope.Pages != nil {
		result.Pages = append(result.Pages, *scope.Pages...)
	}
	if scope.Overrides != nil {
		for _, override := range *scope.Overrides {
			result.Overrides = append(result.Overrides, documentdomain.BatchAnchorRef{PagePath: override.PagePath, Anchor: override.Anchor})
		}
	}
	if err := result.Validate(); err != nil {
		return documentdomain.BatchScope{}, err
	}
	return result, nil
}

func batchScopeToAPI(scope documentdomain.BatchScope) api.AdminDocumentBatchScope {
	result := api.AdminDocumentBatchScope{Kind: api.AdminDocumentBatchScopeKind(scope.Kind)}
	if len(scope.Sections) > 0 {
		result.Sections = &scope.Sections
	}
	if len(scope.Pages) > 0 {
		result.Pages = &scope.Pages
	}
	if len(scope.Overrides) > 0 {
		overrides := make([]api.AdminDocumentBatchAnchorRef, 0, len(scope.Overrides))
		for _, override := range scope.Overrides {
			overrides = append(overrides, api.AdminDocumentBatchAnchorRef{PagePath: override.PagePath, Anchor: override.Anchor})
		}
		result.Overrides = &overrides
	}
	return result
}

// runBatchAction binds the required reason, records the acting administrator
// through the verb's audit, and answers with a fresh batch detail.
func (h *Handler) runBatchAction(c *gin.Context, batchID string, run func(ctx context.Context, actorID, reason string) (documentdomain.DocumentBatch, documentdomain.BatchItemCounts, error)) {
	if !h.requireDocumentationProduct(c) {
		return
	}
	if _, _, err := h.db.DocumentPractice.GetBatch(c.Request.Context(), batchID); err != nil {
		if errors.Is(err, documentdomain.ErrWorkflowNotFound) {
			c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "document batch not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: err.Error()})
		return
	}
	var request api.AdminWorkflowReasonRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: "reason is required"})
		return
	}
	reason := request.Reason
	if len(reason) == 0 || len(reason) > 500 {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: "reason must be between 1 and 500 characters"})
		return
	}
	actorID, _ := c.Get("user_id")
	actor, _ := actorID.(string)
	batch, counts, err := run(c.Request.Context(), actor, reason)
	if err != nil {
		if errors.Is(err, documentdomain.ErrWorkflowConflict) {
			c.JSON(http.StatusConflict, api.ErrorResponse{Error: err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: err.Error()})
		return
	}
	c.JSON(http.StatusOK, adminDocumentBatch(batch, counts))
}

// PauseAdminDocumentationBatch stops new ignitions for a Running batch.
func (h *Handler) PauseAdminDocumentationBatch(c *gin.Context, batchID string) {
	h.runBatchAction(c, batchID, func(ctx context.Context, actorID, reason string) (documentdomain.DocumentBatch, documentdomain.BatchItemCounts, error) {
		return h.documentationBatches.PauseBatch(ctx, batchID, actorID, reason)
	})
}

// ResumeAdminDocumentationBatch restarts scheduling for a Paused batch.
func (h *Handler) ResumeAdminDocumentationBatch(c *gin.Context, batchID string) {
	h.runBatchAction(c, batchID, func(ctx context.Context, actorID, reason string) (documentdomain.DocumentBatch, documentdomain.BatchItemCounts, error) {
		return h.documentationBatches.ResumeBatch(ctx, batchID, actorID, reason)
	})
}

// CancelAdminDocumentationBatch cancels a batch; not-yet-started items are
// cancelled and in-flight items run to their terminal states.
func (h *Handler) CancelAdminDocumentationBatch(c *gin.Context, batchID string) {
	h.runBatchAction(c, batchID, func(ctx context.Context, actorID, reason string) (documentdomain.DocumentBatch, documentdomain.BatchItemCounts, error) {
		return h.documentationBatches.CancelBatch(ctx, batchID, actorID, reason)
	})
}

// RetryFailedAdminDocumentationBatchItems restarts a live batch's failed
// workflows and re-enqueues their items.
func (h *Handler) RetryFailedAdminDocumentationBatchItems(c *gin.Context, batchID string) {
	h.runBatchAction(c, batchID, func(ctx context.Context, actorID, reason string) (documentdomain.DocumentBatch, documentdomain.BatchItemCounts, error) {
		batch, counts, _, err := h.documentationBatches.RetryFailedItems(ctx, batchID, actorID, reason)
		return batch, counts, err
	})
}

type batchItemCursorToken struct {
	Ordinal int `json:"o"`
}

func encodeBatchItemCursor(ordinal int) *string {
	payload, err := json.Marshal(batchItemCursorToken{Ordinal: ordinal})
	if err != nil {
		return nil
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	return &encoded
}

func parseBatchItemCursor(raw *string) (*documentdomain.BatchItemCursor, error) {
	if raw == nil || *raw == "" {
		return nil, nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(*raw)
	if err != nil {
		return nil, errors.New("batch item cursor is invalid")
	}
	var token batchItemCursorToken
	if err := json.Unmarshal(payload, &token); err != nil || token.Ordinal < 0 {
		return nil, errors.New("batch item cursor is invalid")
	}
	return &documentdomain.BatchItemCursor{Ordinal: token.Ordinal}, nil
}

func intValue(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}
