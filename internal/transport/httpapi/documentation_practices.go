package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/breakfix/breakfix/internal/adapter/postgres"
	documentdomain "github.com/breakfix/breakfix/internal/domain/documentpractice"
	"github.com/breakfix/breakfix/internal/domain/runnable"
	api "github.com/breakfix/breakfix/internal/transport/httpapi/generated"
	"github.com/gin-gonic/gin"
)

// postgresPracticeReader adapts the store to the reader-facing practice port.
type postgresPracticeReader struct {
	store *postgres.Store
}

func (r postgresPracticeReader) ListPublishedPracticesForPage(ctx context.Context, sourceID, commit, language, pagePath string) ([]postgres.PublishedPracticeSummary, error) {
	return r.store.DocumentPractice.ListPublishedPracticesForPage(ctx, sourceID, commit, language, pagePath)
}

func (r postgresPracticeReader) GetPublishedPractice(ctx context.Context, practiceID string) (documentdomain.PracticeRevision, error) {
	return r.store.DocumentPractice.GetPublishedPractice(ctx, practiceID)
}

func (r postgresPracticeReader) ResolveRunnableRevision(ctx context.Context, id, digest string) (runnable.RunnableRevision, error) {
	return r.store.Runnable.ResolveRunnableRevision(ctx, id, digest)
}

// ListDocumentationPractices serves the practices anchored on one parsed
// page. Like the page itself this is a public read scoped to the pinned
// library identity, conditioned on the digest of the returned set.
func (h *Handler) ListDocumentationPractices(c *gin.Context) {
	if h.documentationLibrary == nil || h.documentationReader == nil {
		c.JSON(http.StatusServiceUnavailable, api.ErrorResponse{Error: "documentation practices are unavailable"})
		return
	}
	pinned := h.documentationLibrary.PinnedContext()
	pagePath := c.Query("path")
	summaries, err := h.documentationReader.ListPublishedPracticesForPage(c.Request.Context(), pinned.SourceID, pinned.Commit, pinned.Language, pagePath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: "documentation practices query failed"})
		return
	}
	practices := make([]api.DocumentationPracticeSummary, 0, len(summaries))
	for _, summary := range summaries {
		practices = append(practices, api.DocumentationPracticeSummary{Anchor: summary.Anchor, PracticeId: summary.PracticeID, Title: summary.Title})
	}
	digest := documentationPracticeSetDigest(practices)
	c.Header("ETag", `"`+digest+`"`)
	if c.Request.Header.Get("If-None-Match") == `"`+digest+`"` {
		c.Status(http.StatusNotModified)
		c.Writer.WriteHeaderNow()
		return
	}
	c.JSON(http.StatusOK, api.DocumentationPracticesResponse{Digest: digest, Practices: practices})
}

// documentationPracticeSetDigest digests the canonical encoding of the
// returned set so the ETag changes whenever any practice entry does.
func documentationPracticeSetDigest(practices []api.DocumentationPracticeSummary) string {
	encoded, err := json.Marshal(practices)
	if err != nil {
		// api.DocumentationPracticeSummary is plain JSON data; failure is a
		// programming error, not a request condition.
		panic(err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// GetDocumentationPractice serves one practice's reader projection with its
// runtime summary. Unknown IDs and projections-less records are
// indistinguishable: both report not found.
func (h *Handler) GetDocumentationPractice(c *gin.Context) {
	if h.documentationReader == nil {
		c.JSON(http.StatusServiceUnavailable, api.ErrorResponse{Error: "documentation practices are unavailable"})
		return
	}
	practiceID := c.Param("id")
	revision, err := h.documentationReader.GetPublishedPractice(c.Request.Context(), practiceID)
	if errors.Is(err, postgres.ErrPublishedPracticeNotFound) {
		c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "documentation practice is unavailable"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: "documentation practice query failed"})
		return
	}
	projection := revision.ReaderProjection
	profile, err := h.documentationReader.ResolveRunnableRevision(c.Request.Context(), revision.RunnableRevisionRef.ID, revision.RunnableRevisionRef.Digest)
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: "documentation practice runtime is unavailable"})
		return
	}
	c.JSON(http.StatusOK, api.DocumentationPracticeDetail{
		Id:           revision.ID,
		Title:        projection.Title,
		Objective:    projection.Objective,
		Boundary:     projection.Boundary,
		Steps:        projection.Steps,
		Observations: projection.Observations,
		Runtime: api.DocumentationPracticeRuntime{
			Name:      string(profile.Spec.RuntimeProfile.Runtime),
			BaseImage: profile.Spec.RuntimeProfile.BaseImage,
		},
	})
}
