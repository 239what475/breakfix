package httpapi

import (
	"fmt"
	"net/http"
	"time"

	"log/slog"

	"github.com/breakfix/breakfix/internal/adapter/auth"
	"github.com/breakfix/breakfix/internal/adapter/postgres"
	api "github.com/breakfix/breakfix/internal/transport/httpapi/generated"
	"github.com/breakfix/breakfix/internal/transport/httpapi/middleware"
	"github.com/gin-gonic/gin"
)

type adminTOTPResetRequest struct {
	Password string `json:"password"`
}

func (h *Handler) Register(c *gin.Context) {
	if !h.allowRegistration {
		c.JSON(http.StatusForbidden, api.ErrorResponse{Error: "registration is disabled"})
		return
	}
	var req api.RegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: err.Error()})
		return
	}
	if len(req.Username) < 2 {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: "username too short"})
		return
	}
	if len(req.Password) < 6 {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: "password too short (min 6)"})
		return
	}
	if _, err := h.db.Identity.GetUserBySubject(req.Username); err == nil {
		c.JSON(http.StatusConflict, api.ErrorResponse{Error: "user already exists"})
		return
	}

	passwordHash, err := auth.HashPassword(req.Password)
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: "failed to hash password"})
		return
	}
	secret, url, err := auth.GenerateTOTPSecret(req.Username)
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: "failed to generate TOTP"})
		return
	}

	id := fmt.Sprintf("u-%d", time.Now().UnixNano())
	if _, err = h.db.Identity.CreateUserWithAuth(c.Request.Context(), id, req.Username, passwordHash, secret); err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: "failed to create user"})
		return
	}

	slog.Info("user registered", "user", req.Username)
	c.JSON(http.StatusCreated, api.RegisterResponse{
		TotpSecret: secret,
		TotpUrl:    url,
	})
}

func (h *Handler) Login(c *gin.Context) {
	var req api.LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: err.Error()})
		return
	}

	user, err := h.db.Identity.GetUserBySubject(req.Username)
	if err != nil {
		c.JSON(http.StatusUnauthorized, api.ErrorResponse{Error: "invalid credentials"})
		return
	}
	if !auth.CheckPassword(user.PasswordHash, req.Password) {
		c.JSON(http.StatusUnauthorized, api.ErrorResponse{Error: "invalid credentials"})
		return
	}
	if !auth.ValidateTOTP(user.TOTPSecret, req.TotpCode) {
		c.JSON(http.StatusUnauthorized, api.ErrorResponse{Error: "invalid TOTP code"})
		return
	}

	token, err := middleware.GenerateJWT(user.ID, user.Name, user.Role, h.jwtSecret)
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: "failed to generate token"})
		return
	}

	slog.Info("user logged in", "user", req.Username)
	c.JSON(http.StatusOK, api.LoginResponse{
		Token:  token,
		UserId: user.ID,
		Name:   user.Name,
	})
}

func (h *Handler) getUser(c *gin.Context) *postgres.User {
	uid, exists := c.Get("user_id")
	if !exists {
		return nil
	}
	user, err := h.db.Identity.GetUserByID(uid.(string))
	if err != nil {
		return nil
	}
	return user
}

func (h *Handler) requireUser(c *gin.Context) *postgres.User {
	user := h.getUser(c)
	if user == nil {
		c.JSON(http.StatusUnauthorized, api.ErrorResponse{Error: "login required"})
		return nil
	}
	return user
}

func (h *Handler) ListAdminUsers(c *gin.Context) {
	if h == nil || h.db == nil {
		c.JSON(http.StatusServiceUnavailable, api.ErrorResponse{Error: "identity store unavailable"})
		return
	}
	users, err := h.db.Identity.ListUsers(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"users": users})
}

// ResetAdminUserTOTP requires the caller's own password so a stolen bearer
// token alone cannot silently replace a victim's second factor. The target's
// previous TOTP secret stops validating the moment the update commits.
func (h *Handler) ResetAdminUserTOTP(c *gin.Context) {
	if h == nil || h.db == nil {
		c.JSON(http.StatusServiceUnavailable, api.ErrorResponse{Error: "identity store unavailable"})
		return
	}
	var request adminTOTPResetRequest
	if err := c.ShouldBindJSON(&request); err != nil || request.Password == "" {
		c.JSON(http.StatusForbidden, api.ErrorResponse{Error: "password confirmation required"})
		return
	}
	actor := h.getUser(c)
	if actor == nil || !auth.CheckPassword(actor.PasswordHash, request.Password) {
		c.JSON(http.StatusForbidden, api.ErrorResponse{Error: "password confirmation failed"})
		return
	}
	target, err := h.db.Identity.GetUserByID(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "user not found"})
		return
	}
	secret, url, err := auth.GenerateTOTPSecret(target.Subject)
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: "failed to generate TOTP"})
		return
	}
	if err := h.db.Identity.UpdateTOTPSecret(c.Request.Context(), target.ID, secret); err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: "failed to reset TOTP"})
		return
	}
	c.JSON(http.StatusOK, api.RegisterResponse{TotpSecret: secret, TotpUrl: url})
}
