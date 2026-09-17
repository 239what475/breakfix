package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/adapter/auth"
	"github.com/breakfix/breakfix/internal/adapter/postgres"
	"github.com/breakfix/breakfix/internal/bootstrap/config"
	testpostgres "github.com/breakfix/breakfix/internal/testkit/postgres"
	api "github.com/breakfix/breakfix/internal/transport/httpapi/generated"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/pquerna/otp/totp"
)

type authTestServer struct {
	router *gin.Engine
	db     *postgres.Store
	cfg    config.Config
}

func newAuthTestServer(t *testing.T, mutate func(*config.Config)) *authTestServer {
	t.Helper()
	database := testpostgres.New(t)
	cfg := config.Config{JWTSecret: "auth-flow-jwt-secret", AllowRegistration: true}
	if mutate != nil {
		mutate(&cfg)
	}
	handler, err := NewHandlerWithDependencies(database, nil, cfg, Dependencies{})
	if err != nil {
		t.Fatalf("create auth API handler: %v", err)
	}
	router, err := SetupRouter(handler, cfg, nil)
	if err != nil {
		t.Fatalf("register auth API routes: %v", err)
	}
	return &authTestServer{router: router, db: database, cfg: cfg}
}

func (s *authTestServer) do(t *testing.T, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("encode request body: %v", err)
		}
		reader = strings.NewReader(string(payload))
	} else {
		reader = strings.NewReader("")
	}
	request := httptest.NewRequest(method, path, reader)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	s.router.ServeHTTP(recorder, request)
	return recorder
}

func (s *authTestServer) register(t *testing.T, username, password string) api.RegisterResponse {
	t.Helper()
	recorder := s.do(t, http.MethodPost, "/api/auth/register", "", api.RegisterRequest{Username: username, Password: password})
	if recorder.Code != http.StatusCreated {
		t.Fatalf("register %q = %d: %s", username, recorder.Code, recorder.Body.String())
	}
	var response api.RegisterResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode register response: %v", err)
	}
	return response
}

func (s *authTestServer) login(t *testing.T, username, password, totpSecret string) string {
	t.Helper()
	code, err := totp.GenerateCode(totpSecret, time.Now())
	if err != nil {
		t.Fatalf("generate totp code: %v", err)
	}
	recorder := s.do(t, http.MethodPost, "/api/auth/login", "", api.LoginRequest{Username: username, Password: password, TotpCode: code})
	if recorder.Code != http.StatusOK {
		t.Fatalf("login %q = %d: %s", username, recorder.Code, recorder.Body.String())
	}
	var response api.LoginResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode login response: %v", err)
	}
	return response.Token
}

// legacyToken mints a pre-role-model JWT: the payload has no role field at all.
func legacyToken(t *testing.T, secret, userID, name string) string {
	t.Helper()
	claims := jwt.MapClaims{"uid": userID, "name": name, "exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix()}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("mint legacy token: %v", err)
	}
	return signed
}

func jwtPayloadRole(t *testing.T, token string) string {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token %q is not a JWT", token)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode jwt payload: %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(payload, &fields); err != nil {
		t.Fatalf("decode jwt payload json: %v", err)
	}
	role, _ := fields["role"].(string)
	return role
}

func TestFirstRegisteredUserBecomesAdminAndLaterOnesStayUsers(t *testing.T) {
	server := newAuthTestServer(t, nil)
	adminRegister := server.register(t, "alice", "alice-password")
	adminToken := server.login(t, "alice", "alice-password", adminRegister.TotpSecret)
	if role := jwtPayloadRole(t, adminToken); role != "admin" {
		t.Fatalf("first user role claim = %q, want admin", role)
	}
	userRegister := server.register(t, "bob", "bob-password")
	userToken := server.login(t, "bob", "bob-password", userRegister.TotpSecret)
	if role := jwtPayloadRole(t, userToken); role != "user" {
		t.Fatalf("second user role claim = %q, want user", role)
	}

	recorder := server.do(t, http.MethodGet, "/api/admin/users", adminToken, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("admin list users = %d: %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "password_hash") || strings.Contains(recorder.Body.String(), "totp_secret") {
		t.Fatalf("admin user list leaked credential fields: %s", recorder.Body.String())
	}
	var list struct {
		Users []api.AdminUser `json:"users"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode admin user list: %v", err)
	}
	if len(list.Users) != 2 {
		t.Fatalf("admin user list = %#v, want two accounts", list.Users)
	}
	if list.Users[0].Role != "admin" || list.Users[1].Role != "user" {
		t.Fatalf("admin user list roles = %q, %q", list.Users[0].Role, list.Users[1].Role)
	}
}

func TestLegacyTokenWithoutRoleClaimIsAnOrdinaryUser(t *testing.T) {
	server := newAuthTestServer(t, nil)
	adminRegister := server.register(t, "alice", "alice-password")
	adminToken := server.login(t, "alice", "alice-password", adminRegister.TotpSecret)
	userRegister := server.register(t, "bob", "bob-password")
	userToken := server.login(t, "bob", "bob-password", userRegister.TotpSecret)

	recorder := server.do(t, http.MethodGet, "/api/admin/users", adminToken, nil)
	var list struct {
		Users []api.AdminUser `json:"users"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode admin user list: %v", err)
	}
	var bobID string
	for _, user := range list.Users {
		if user.Subject == "bob" {
			bobID = user.Id
		}
	}
	if bobID == "" {
		t.Fatalf("target user missing from admin list: %s", recorder.Body.String())
	}

	// A pre-role-model token carries no role field in its payload.
	legacyBob := legacyToken(t, server.cfg.JWTSecret, bobID, "Bob")
	if role := jwtPayloadRole(t, legacyBob); role != "" {
		t.Fatalf("legacy token role claim = %q, want absent", role)
	}
	recorder = server.do(t, http.MethodGet, "/api/admin/users", legacyBob, nil)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("legacy token on admin endpoint = %d, want 403: %s", recorder.Code, recorder.Body.String())
	}
	// The same legacy token still authenticates ordinary user endpoints.
	recorder = server.do(t, http.MethodPost, "/api/authoring/sessions", legacyBob, nil)
	if recorder.Code == http.StatusUnauthorized || recorder.Code == http.StatusForbidden {
		t.Fatalf("legacy token on user endpoint = %d, want authenticated", recorder.Code)
	}

	recorder = server.do(t, http.MethodGet, "/api/admin/users", userToken, nil)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("user token on admin endpoint = %d, want 403", recorder.Code)
	}
	recorder = server.do(t, http.MethodPost, "/api/documentation/practice", userToken, nil)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("user token on practice ignition = %d, want 403: %s", recorder.Code, recorder.Body.String())
	}
	recorder = server.do(t, http.MethodPost, "/api/admin/users/u-legacy/totp-reset", userToken, nil)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("user token on totp reset = %d, want 403", recorder.Code)
	}

	recorder = server.do(t, http.MethodPost, "/api/documentation/practice", adminToken, nil)
	if recorder.Code == http.StatusForbidden || recorder.Code == http.StatusUnauthorized {
		t.Fatalf("admin token on practice ignition = %d, want past authorization", recorder.Code)
	}
}

func TestRegistrationCanBeDisabled(t *testing.T) {
	server := newAuthTestServer(t, func(cfg *config.Config) { cfg.AllowRegistration = false })
	// Seed an account directly so the login path stays verifiable.
	secret := mustTOTPSecret(t, "seed")
	if _, err := server.db.Identity.CreateUserWithAuth(context.Background(), "u-seed", "seed", mustHash(t, "seed-password"), secret); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	recorder := server.do(t, http.MethodPost, "/api/auth/register", "", api.RegisterRequest{Username: "latecomer", Password: "latecomer-password"})
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("disabled registration = %d, want 403: %s", recorder.Code, recorder.Body.String())
	}
	server.login(t, "seed", "seed-password", secret)
}

func TestAdminTOTPResetRequiresPasswordConfirmationAndRotatesTheSecret(t *testing.T) {
	server := newAuthTestServer(t, nil)
	adminRegister := server.register(t, "alice", "alice-password")
	adminToken := server.login(t, "alice", "alice-password", adminRegister.TotpSecret)
	userRegister := server.register(t, "bob", "bob-password")

	// Resolve the target id through the admin list endpoint itself.
	recorder := server.do(t, http.MethodGet, "/api/admin/users", adminToken, nil)
	var list struct {
		Users []api.AdminUser `json:"users"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode admin user list: %v", err)
	}
	var bobID string
	for _, user := range list.Users {
		if user.Subject == "bob" {
			bobID = user.Id
		}
	}
	if bobID == "" {
		t.Fatalf("target user missing from admin list: %s", recorder.Body.String())
	}

	recorder = server.do(t, http.MethodPost, "/api/admin/users/"+bobID+"/totp-reset", adminToken, adminTOTPResetRequest{Password: "wrong-password"})
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("totp reset with wrong password = %d, want 403", recorder.Code)
	}
	if !verifyTOTP(t, userRegister.TotpSecret) {
		t.Fatalf("bob secret no longer validates after failed confirmation")
	}

	recorder = server.do(t, http.MethodPost, "/api/admin/users/u-missing/totp-reset", adminToken, adminTOTPResetRequest{Password: "alice-password"})
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("totp reset for missing user = %d, want 404", recorder.Code)
	}

	recorder = server.do(t, http.MethodPost, "/api/admin/users/"+bobID+"/totp-reset", adminToken, adminTOTPResetRequest{Password: "alice-password"})
	if recorder.Code != http.StatusOK {
		t.Fatalf("totp reset = %d: %s", recorder.Code, recorder.Body.String())
	}
	var rotated api.RegisterResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &rotated); err != nil {
		t.Fatalf("decode totp reset response: %v", err)
	}
	if rotated.TotpSecret == "" || rotated.TotpUrl == "" {
		t.Fatalf("totp reset response is incomplete: %#v", rotated)
	}
	if rotated.TotpSecret == userRegister.TotpSecret {
		t.Fatalf("totp reset returned the previous secret")
	}
	// The old secret stops validating; the new one logs in.
	code, err := totp.GenerateCode(userRegister.TotpSecret, time.Now())
	if err != nil {
		t.Fatalf("generate old totp code: %v", err)
	}
	recorder = server.do(t, http.MethodPost, "/api/auth/login", "", api.LoginRequest{Username: "bob", Password: "bob-password", TotpCode: code})
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("login with old totp = %d, want 401", recorder.Code)
	}
	server.login(t, "bob", "bob-password", rotated.TotpSecret)
}

func verifyTOTP(t *testing.T, secret string) bool {
	t.Helper()
	code, err := totp.GenerateCode(secret, time.Now())
	if err != nil {
		t.Fatalf("generate totp code: %v", err)
	}
	return totp.Validate(code, secret)
}

func mustHash(t *testing.T, password string) string {
	t.Helper()
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	return hash
}

func mustTOTPSecret(t *testing.T, username string) string {
	t.Helper()
	secret, _, err := auth.GenerateTOTPSecret(username)
	if err != nil {
		t.Fatalf("generate totp secret: %v", err)
	}
	return secret
}
