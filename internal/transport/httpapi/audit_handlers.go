package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/breakfix/breakfix/internal/adapter/postgres"
	"github.com/breakfix/breakfix/internal/domain/audit"
	api "github.com/breakfix/breakfix/internal/transport/httpapi/generated"
	"github.com/gin-gonic/gin"
)

const adminAuditInitialLimit = 20

// ListAdminAudit pages the human action ledger newest-first. The endpoint is
// strictly read-only: no administrative action is ever audited here.
func (h *Handler) ListAdminAudit(c *gin.Context, params api.ListAdminAuditParams) {
	if h == nil || h.db == nil {
		c.JSON(http.StatusServiceUnavailable, api.ErrorResponse{Error: "audit ledger is unavailable"})
		return
	}
	limit := adminAuditInitialLimit
	if params.Limit != nil {
		limit = *params.Limit
	}
	if limit < 1 || limit > 100 {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: "audit page limit must be between 1 and 100"})
		return
	}
	cursor, err := parseAdminAuditCursor(params.Cursor)
	if err != nil {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: err.Error()})
		return
	}
	filter := postgres.HumanActionFilter{Limit: limit + 1, Cursor: cursor}
	if params.Action != nil {
		filter.Action = *params.Action
	}
	if params.UserId != nil {
		filter.UserID = *params.UserId
	}
	rows, err := h.db.Audit.ListHumanActions(c.Request.Context(), filter)
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: err.Error()})
		return
	}
	page := api.AdminAuditPage{Items: make([]api.AdminHumanAction, 0, min(limit, len(rows)))}
	if len(rows) > limit {
		last := rows[limit-1]
		page.NextCursor = encodeAdminAuditCursor(last.CreatedAt, last.ID)
		rows = rows[:limit]
	}
	for _, action := range rows {
		detail := map[string]any{}
		if len(action.Detail) > 0 {
			_ = json.Unmarshal(action.Detail, &detail)
		}
		page.Items = append(page.Items, api.AdminHumanAction{
			Id:         action.ID,
			UserId:     action.UserID,
			Action:     api.AdminHumanActionAction(action.Action),
			TargetType: action.TargetType,
			TargetId:   action.TargetID,
			Detail:     detail,
			CreatedAt:  action.CreatedAt,
		})
	}
	c.JSON(http.StatusOK, page)
}

type adminAuditCursorToken struct {
	CreatedAt string `json:"c"`
	ID        string `json:"i"`
}

func encodeAdminAuditCursor(createdAt time.Time, id string) *string {
	payload, err := json.Marshal(adminAuditCursorToken{CreatedAt: createdAt.UTC().Format(time.RFC3339Nano), ID: id})
	if err != nil {
		return nil
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	return &encoded
}

func parseAdminAuditCursor(raw *string) (*audit.HumanActionCursor, error) {
	if raw == nil || *raw == "" {
		return nil, nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(*raw)
	if err != nil {
		return nil, fmt.Errorf("audit cursor is invalid")
	}
	var token adminAuditCursorToken
	if err := json.Unmarshal(payload, &token); err != nil {
		return nil, fmt.Errorf("audit cursor is invalid")
	}
	createdAt, err := time.Parse(time.RFC3339Nano, token.CreatedAt)
	if err != nil || createdAt.IsZero() || token.ID == "" {
		return nil, fmt.Errorf("audit cursor is invalid")
	}
	return &audit.HumanActionCursor{CreatedAt: createdAt.UTC(), ID: token.ID}, nil
}
