package httpapi

import (
	"net/http"
	"sort"
	"strings"
	"time"

	api "github.com/breakfix/breakfix/internal/transport/httpapi/generated"
	"github.com/gin-gonic/gin"
)

const adminCorpusInitialLimit = 50

func timeNowUTC() time.Time { return time.Now().UTC() }

// ListAdminDocumentationCorpus projects the deployment's corpus for the admin
// console: library pages matching an optional tree section and/or title
// search, each with a workflow state rollup. Search and filtering run
// server-side; titles come from the library manifests.
func (h *Handler) ListAdminDocumentationCorpus(c *gin.Context, params api.ListAdminDocumentationCorpusParams) {
	if !h.requireDocumentationProduct(c) {
		return
	}
	limit := adminCorpusInitialLimit
	if params.Limit != nil {
		limit = *params.Limit
	}
	if limit < 1 || limit > 200 {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: "corpus page limit must be between 1 and 200"})
		return
	}
	if h.documentationLibrary == nil {
		c.JSON(http.StatusOK, api.AdminDocumentationCorpusPage{Items: []api.AdminDocumentationCorpusPageRow{}})
		return
	}
	titles := h.documentationLibrary.DocumentTitles()

	// Section scoping is path scoping: every page under the tree node.
	section := ""
	if params.Section != nil {
		section = strings.TrimSuffix(strings.TrimSpace(*params.Section), "/")
	}
	search := ""
	if params.Search != nil {
		search = strings.ToLower(strings.TrimSpace(*params.Search))
	}
	cursor := ""
	if params.Cursor != nil {
		cursor = *params.Cursor
	}

	matched := make([]string, 0, len(titles))
	for path := range titles {
		if section != "" && path != section && !strings.HasPrefix(path, section+"/") {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(titles[path]), search) {
			continue
		}
		if cursor != "" && path <= cursor {
			continue
		}
		matched = append(matched, path)
	}
	sort.Strings(matched)

	nextCursor := ""
	if len(matched) > limit {
		nextCursor = matched[limit-1]
		matched = matched[:limit]
	}

	identity := h.documentationLibrary.PinnedContext()
	now := timeNowUTC()
	counts, err := h.db.DocumentPractice.SummarizeWorkflowStatesByPages(c.Request.Context(), identity, matched, now.Add(-h.agentStuckAfter), now.Add(-runnableActionStuckBudget))
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: err.Error()})
		return
	}

	page := api.AdminDocumentationCorpusPage{Items: make([]api.AdminDocumentationCorpusPageRow, 0, len(matched))}
	for _, path := range matched {
		count := counts[path]
		if params.Failures != nil && *params.Failures && count.Stuck == 0 {
			continue
		}
		page.Items = append(page.Items, api.AdminDocumentationCorpusPageRow{
			PagePath:   path,
			Title:      titles[path],
			Total:      count.Total,
			Published:  count.Published,
			Failed:     count.Failed,
			NoPractice: count.NoPractice,
			InProgress: count.InProgress,
			Stuck:      count.Stuck,
		})
	}
	if nextCursor != "" {
		page.NextCursor = &nextCursor
	}
	c.JSON(http.StatusOK, page)
}
