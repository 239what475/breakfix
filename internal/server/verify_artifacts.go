package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/api"
	"github.com/breakfix/breakfix/internal/challenge"
	"github.com/breakfix/breakfix/internal/registry"
	"github.com/breakfix/breakfix/internal/verification"
	"github.com/gin-gonic/gin"
)

// Verify build artifacts are a deliberately narrow handoff between the
// untrusted Builder, trusted Publisher, and Server. They never use the broad
// internal API credential used by Agent Workers.
func (h *Handler) DownloadVerifyBuildSubmission(c *gin.Context) {
	grant, finish, ok := h.acquireVerifyGrant(c, verification.GrantSubmissionDownload)
	if !ok {
		return
	}
	completed := false
	defer func() { _ = finish(completed) }()
	if strings.TrimSpace(grant.SubmissionID) == "" {
		c.JSON(http.StatusForbidden, api.ErrorResponse{Error: "submission grant is incomplete"})
		return
	}
	path := challenge.SubmissionPath(h.dataDir, grant.SubmissionID)
	h.sendVerifyArtifact(c, path, grant.SubmissionID+".tar.gz", "application/gzip", &completed)
}

func (h *Handler) DownloadVerifyBuildBase(c *gin.Context) {
	grant, finish, ok := h.acquireVerifyGrant(c, verification.GrantBaseDownload)
	if !ok {
		return
	}
	completed := false
	defer func() { _ = finish(completed) }()
	path, err := h.verifyBaseArchive(c.Request.Context(), grant.Runtime)
	if err != nil {
		c.JSON(http.StatusBadGateway, api.ErrorResponse{Error: fmt.Sprintf("prepare verification base archive: %v", err)})
		return
	}
	h.sendVerifyArtifact(c, path, "base.oci.tar", "application/vnd.oci.image.tar", &completed)
}

func (h *Handler) UploadVerifyBuildImage(c *gin.Context) {
	_, finish, ok := h.acquireVerifyGrant(c, verification.GrantOCIUpload)
	if !ok {
		return
	}
	completed := false
	defer func() { _ = finish(completed) }()
	if c.Request.Body == nil {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: "OCI archive body is required"})
		return
	}
	dir := h.verifyBuildDirectory(c.Param("taskID"))
	if err := os.MkdirAll(dir, 0755); err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("create verification artifact directory: %v", err)})
		return
	}
	target := filepath.Join(dir, "image.oci.tar")
	if _, err := os.Stat(target); err == nil {
		c.JSON(http.StatusConflict, api.ErrorResponse{Error: "OCI archive already uploaded"})
		return
	} else if !errors.Is(err, os.ErrNotExist) {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("stat OCI archive: %v", err)})
		return
	}
	temporary, err := os.CreateTemp(dir, ".image-*.tmp")
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("create OCI archive: %v", err)})
		return
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath) //nolint:errcheck
	if _, err := io.Copy(temporary, c.Request.Body); err != nil {
		_ = temporary.Close()
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: fmt.Sprintf("write OCI archive: %v", err)})
		return
	}
	if err := temporary.Close(); err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("close OCI archive: %v", err)})
		return
	}
	if err := os.Rename(temporaryPath, target); err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("store OCI archive: %v", err)})
		return
	}
	completed = true
	c.Status(http.StatusCreated)
}

func (h *Handler) DownloadVerifyBuildImage(c *gin.Context) {
	_, finish, ok := h.acquireVerifyGrant(c, verification.GrantOCIDownload)
	if !ok {
		return
	}
	completed := false
	defer func() { _ = finish(completed) }()
	path := filepath.Join(h.verifyBuildDirectory(c.Param("taskID")), "image.oci.tar")
	h.sendVerifyArtifact(c, path, "image.oci.tar", "application/vnd.oci.image.tar", &completed)
}

func (h *Handler) sendVerifyArtifact(c *gin.Context, path, filename, contentType string, completed *bool) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "verification artifact not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("open verification artifact: %v", err)})
		return
	}
	defer f.Close()
	c.Header("Content-Type", contentType)
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%s", filename))
	c.Status(http.StatusOK)
	if _, err := io.Copy(c.Writer, f); err == nil {
		*completed = true
	}
}

func (h *Handler) acquireVerifyGrant(c *gin.Context, operation verification.GrantOperation) (verification.Grant, func(bool) error, bool) {
	if len(h.verificationGrantKey) == 0 {
		c.JSON(http.StatusServiceUnavailable, api.ErrorResponse{Error: "verification build handoff is disabled"})
		return verification.Grant{}, nil, false
	}
	token := strings.TrimSpace(strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer "))
	if token == "" {
		c.JSON(http.StatusUnauthorized, api.ErrorResponse{Error: "verification grant is required"})
		return verification.Grant{}, nil, false
	}
	taskID := strings.TrimSpace(c.Param("taskID"))
	if !challenge.ValidID(taskID) {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: "invalid verify task id"})
		return verification.Grant{}, nil, false
	}
	grant, err := verification.VerifyGrant(h.verificationGrantKey, token, operation, taskID, time.Now())
	if err != nil {
		c.JSON(http.StatusUnauthorized, api.ErrorResponse{Error: err.Error()})
		return verification.Grant{}, nil, false
	}
	finish, err := h.reserveVerifyGrant(taskID, token)
	if err != nil {
		c.JSON(http.StatusConflict, api.ErrorResponse{Error: err.Error()})
		return verification.Grant{}, nil, false
	}
	return grant, finish, true
}

func (h *Handler) reserveVerifyGrant(taskID, token string) (func(bool) error, error) {
	dir := filepath.Join(h.verifyBuildDirectory(taskID), "grants")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create grant directory: %w", err)
	}
	fingerprint := verification.GrantFingerprint(token)
	used := filepath.Join(dir, fingerprint+".used")
	if _, err := os.Stat(used); err == nil {
		return nil, fmt.Errorf("verification grant was already used")
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("stat verification grant: %w", err)
	}
	lock := filepath.Join(dir, fingerprint+".lock")
	file, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("verification grant is in use")
		}
		return nil, fmt.Errorf("reserve verification grant: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(lock)
		return nil, fmt.Errorf("close verification grant reservation: %w", err)
	}
	return func(success bool) error {
		defer os.Remove(lock) //nolint:errcheck
		if !success {
			return nil
		}
		marker, err := os.OpenFile(used, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		return marker.Close()
	}, nil
}

func (h *Handler) verifyBuildDirectory(taskID string) string {
	return filepath.Join(h.dataDir, "verify-builds", taskID)
}

func (h *Handler) verifyBaseArchive(ctx context.Context, runtime string) (string, error) {
	if runtime != challenge.RuntimeContainer && runtime != challenge.RuntimeVCluster {
		return "", fmt.Errorf("unsupported verification runtime %q", runtime)
	}
	if err := h.registryCredentials.Validate(); err != nil {
		return "", err
	}
	dir := filepath.Join(h.dataDir, "verify-bases")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	target := filepath.Join(dir, runtime+".oci.tar")
	if info, err := os.Stat(target); err == nil && info.Mode().IsRegular() {
		return target, nil
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	temporary, err := os.CreateTemp(dir, "."+runtime+"-*.tmp")
	if err != nil {
		return "", err
	}
	temporaryPath := temporary.Name()
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return "", err
	}
	defer os.Remove(temporaryPath) //nolint:errcheck
	if err := (registry.Client{Insecure: h.registryInsecure, Credentials: h.registryCredentials}).PullOCIArchive(ctx, verification.BaseImage(h.registryAddr, runtime), temporaryPath); err != nil {
		return "", err
	}
	if err := os.Rename(temporaryPath, target); err != nil {
		if _, statErr := os.Stat(target); statErr == nil {
			return target, nil
		}
		return "", err
	}
	return target, nil
}
