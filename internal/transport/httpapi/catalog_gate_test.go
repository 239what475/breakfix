package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	appcatalog "github.com/breakfix/breakfix/internal/application/catalog"
	catalogdomain "github.com/breakfix/breakfix/internal/domain/catalog"
	"github.com/gin-gonic/gin"
)

func TestCatalogGateWaitsForConfiguredReleaseWithoutAffectingOtherRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	digest := catalogdomain.BundleDigest("sha256:" + strings.Repeat("a", 64))
	store := &catalogGateStore{release: &catalogdomain.Release{
		ID: catalogdomain.ReleaseIDForBundle(digest), BundleDigest: digest, State: catalogdomain.ReleaseInstalling,
		SourceAttempt: 1, NextRunAt: time.Now().UTC(), CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}}
	availability, err := appcatalog.NewAvailability("registry.example.com/breakfix/catalog@"+string(digest), store)
	if err != nil {
		t.Fatalf("create catalog availability: %v", err)
	}
	handler := &Handler{catalog: appcatalog.NewService(t.TempDir(), availability, nil, nil)}
	router := gin.New()
	router.GET("/catalog", handler.requireCatalogReady, func(c *gin.Context) { c.Status(http.StatusNoContent) })
	router.GET("/healthz", func(c *gin.Context) { c.Status(http.StatusOK) })

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/catalog", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("catalog route while release installs = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("unrelated health route = %d, want %d", response.Code, http.StatusOK)
	}

	store.release.State = catalogdomain.ReleaseReady
	store.release.CommitID = catalogdomain.CommitIDForRelease(store.release.ID)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/catalog", nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("catalog route after release ready = %d, want %d", response.Code, http.StatusNoContent)
	}
}

type catalogGateStore struct {
	release *catalogdomain.Release
}

func (s *catalogGateStore) ReleaseByDigest(_ context.Context, digest catalogdomain.BundleDigest) (*catalogdomain.Release, error) {
	if s.release == nil || s.release.BundleDigest != digest {
		return nil, catalogdomain.ErrReleaseNotFound
	}
	return s.release, nil
}
