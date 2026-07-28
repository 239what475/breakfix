package server

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/challenge"
	"github.com/breakfix/breakfix/internal/config"
	"github.com/breakfix/breakfix/internal/verification"
	"github.com/gin-gonic/gin"
)

func TestVerifyBuildArtifactEndpointsRequireTaskBoundSingleUseGrant(t *testing.T) {
	gin.SetMode(gin.TestMode)
	root := t.TempDir()
	const taskID = "verify-sub-example"
	const submissionID = "sub-example"
	if _, err := challenge.SaveSubmissionAtomic(root, submissionID, bytes.NewReader([]byte("candidate"))); err != nil {
		t.Fatal(err)
	}
	key := []byte("test-verification-grant-key")
	handler := NewHandler(nil, nil, config.Config{DataDir: root, VerificationGrantKey: string(key), InternalAPIKey: "broad-internal-key"})
	router := gin.New()
	router.GET("/api/internal/verify-builds/:taskID/submission", handler.DownloadVerifyBuildSubmission)
	router.PUT("/api/internal/verify-builds/:taskID/image", handler.UploadVerifyBuildImage)
	router.GET("/api/internal/verify-builds/:taskID/image", handler.DownloadVerifyBuildImage)

	submissionGrant := mustVerificationGrant(t, key, verification.GrantSubmissionDownload, taskID, submissionID)
	request := httptest.NewRequest(http.MethodGet, "/api/internal/verify-builds/"+taskID+"/submission", nil)
	request.Header.Set("Authorization", "Bearer "+submissionGrant)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.String() != "candidate" {
		t.Fatalf("submission handoff = %d %q", response.Code, response.Body.String())
	}

	retry := httptest.NewRequest(http.MethodGet, "/api/internal/verify-builds/"+taskID+"/submission", nil)
	retry.Header.Set("Authorization", "Bearer "+submissionGrant)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, retry)
	if response.Code != http.StatusConflict {
		t.Fatalf("reused submission grant = %d, want %d", response.Code, http.StatusConflict)
	}

	wrongAuth := httptest.NewRequest(http.MethodGet, "/api/internal/verify-builds/"+taskID+"/submission", nil)
	wrongAuth.Header.Set("X-Breakfix-Internal-Key", "broad-internal-key")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, wrongAuth)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("broad internal key reached build handoff: %d", response.Code)
	}

	uploadGrant := mustVerificationGrant(t, key, verification.GrantOCIUpload, taskID, submissionID)
	request = httptest.NewRequest(http.MethodPut, "/api/internal/verify-builds/"+taskID+"/image", bytes.NewReader([]byte("oci-bytes")))
	request.Header.Set("Authorization", "Bearer "+uploadGrant)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("OCI upload = %d: %s", response.Code, response.Body.String())
	}
	stored, err := os.ReadFile(filepath.Join(root, "verify-builds", taskID, "image.oci.tar"))
	if err != nil || string(stored) != "oci-bytes" {
		t.Fatalf("stored OCI artifact = %q, %v", stored, err)
	}

	downloadGrant := mustVerificationGrant(t, key, verification.GrantOCIDownload, taskID, submissionID)
	request = httptest.NewRequest(http.MethodGet, "/api/internal/verify-builds/"+taskID+"/image", nil)
	request.Header.Set("Authorization", "Bearer "+downloadGrant)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("OCI download = %d: %s", response.Code, response.Body.String())
	}
	data, err := io.ReadAll(response.Result().Body)
	if err != nil || string(data) != "oci-bytes" {
		t.Fatalf("downloaded OCI artifact = %q, %v", data, err)
	}
}

func mustVerificationGrant(t *testing.T, key []byte, operation verification.GrantOperation, taskID, submissionID string) string {
	t.Helper()
	grant, err := verification.IssueGrant(key, verification.Grant{
		Operation:    operation,
		TaskID:       taskID,
		SubmissionID: submissionID,
		ExpiresAt:    time.Now().Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return grant
}
