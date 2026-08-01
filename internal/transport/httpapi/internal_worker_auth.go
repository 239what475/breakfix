package httpapi

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/breakfix/breakfix/internal/bootstrap/config"
	api "github.com/breakfix/breakfix/internal/transport/httpapi/generated"
	"github.com/gin-gonic/gin"
)

// decodeInternalWorkerRequest is the complete authentication boundary for the
// two fixed background deployments. Server-owned interactive calls never use
// this endpoint family.
func (h *Handler) decodeInternalWorkerRequest(c *gin.Context, expected config.InternalWorkerRole, value any) bool {
	if !h.authorizeInternalWorker(c, expected) {
		return false
	}
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: fmt.Sprintf("invalid internal worker request: %v", err)})
		return false
	}
	if err := requireJSONEOF(decoder); err != nil {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: err.Error()})
		return false
	}
	return true
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err == io.EOF {
		return nil
	} else if err == nil {
		return errors.New("invalid internal worker request: multiple JSON values")
	} else {
		return fmt.Errorf("invalid internal worker request: %w", err)
	}
}

func (h *Handler) authorizeInternalWorker(c *gin.Context, expected config.InternalWorkerRole) bool {
	if strings.TrimSpace(h.internalWorkers.Key(expected)) == "" {
		c.JSON(http.StatusServiceUnavailable, api.ErrorResponse{Error: "internal worker API is disabled"})
		return false
	}
	role, ok := h.internalWorkerRole(c.GetHeader("X-Breakfix-Internal-Key"))
	if !ok {
		c.JSON(http.StatusUnauthorized, api.ErrorResponse{Error: "invalid internal key"})
		return false
	}
	if role != expected {
		c.JSON(http.StatusForbidden, api.ErrorResponse{Error: "internal worker is not authorized for this endpoint"})
		return false
	}
	return true
}

func (h *Handler) internalWorkerRole(value string) (config.InternalWorkerRole, bool) {
	if strings.TrimSpace(value) == "" {
		return "", false
	}
	matches := make([]config.InternalWorkerRole, 0, 1)
	for _, role := range []config.InternalWorkerRole{config.InternalWorkerGenerate, config.InternalWorkerTaxonomy} {
		key := h.internalWorkers.Key(role)
		if key != "" && subtle.ConstantTimeCompare([]byte(value), []byte(key)) == 1 {
			matches = append(matches, role)
		}
	}
	if len(matches) != 1 {
		return "", false
	}
	return matches[0], true
}
