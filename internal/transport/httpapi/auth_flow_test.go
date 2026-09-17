package httpapi

import (
	"context"
	"errors"
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
	"github.com/breakfix/breakfix/internal/domain/audit"
	documentdomain "github.com/breakfix/breakfix/internal/domain/documentpractice"
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

func newAuthTestServer(t *testing.T, mutate func(cfg *config.Config, dependencies *Dependencies, database *postgres.Store)) *authTestServer {
	t.Helper()
	database := testpostgres.New(t)
	cfg := config.Config{JWTSecret: "auth-flow-jwt-secret", AllowRegistration: true}
	dependencies := Dependencies{}
	if mutate != nil {
		mutate(&cfg, &dependencies, database)
	}
	handler, err := NewHandlerWithDependencies(database, nil, cfg, dependencies)
	if err != nil {
		t.Fatalf("create auth API handler: %v", err)
	}
	router, err := SetupRouter(handler, cfg, nil)
	if err != nil {
		t.Fatalf("register auth API routes: %v", err)
	}
	return &authTestServer{router: router, db: database, cfg: cfg}
}

func newAuthTestServerSimple(t *testing.T, mutate func(*config.Config)) *authTestServer {
	t.Helper()
	return newAuthTestServer(t, func(cfg *config.Config, _ *Dependencies, _ *postgres.Store) {
		if mutate != nil {
			mutate(cfg)
		}
	})
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
	server := newAuthTestServerSimple(t, func(cfg *config.Config) { cfg.AllowRegistration = false })
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

// auditRecordingDocumentationApplication mimics the deployment-owned fixed
// application: it forwards the ignition actor and records the human action
// through the real document practice repository.
type auditRecordingDocumentationApplication struct {
	db     *postgres.Store
	actors []string
}

func (a *auditRecordingDocumentationApplication) ForceFailDocumentationWorkflow(context.Context, string, string, *audit.HumanAction) (documentdomain.Workflow, error) {
	return documentdomain.Workflow{}, errors.New("not implemented")
}

func (a *auditRecordingDocumentationApplication) RestartDocumentationWorkflow(context.Context, string, string, *audit.HumanAction) (documentdomain.Workflow, error) {
	return documentdomain.Workflow{}, errors.New("not implemented")
}

func (a *auditRecordingDocumentationApplication) StartDocumentationPractice(ctx context.Context, actorID string) (documentdomain.Workflow, error) {
	a.actors = append(a.actors, actorID)
	now := time.Now().UTC()
	workflow, err := documentdomain.NewWorkflow("document-workflow-01", now)
	if err != nil {
		return documentdomain.Workflow{}, err
	}
	detail, err := json.Marshal(map[string]string{"workflow_id": workflow.ID})
	if err != nil {
		return documentdomain.Workflow{}, err
	}
	action := audit.HumanAction{
		ID:         audit.NewID(now),
		UserID:     actorID,
		Action:     audit.ActionDocumentationPracticeStart,
		TargetType: audit.TargetDocumentWorkflow,
		TargetID:   workflow.ID,
		Detail:     detail,
		CreatedAt:  now,
	}
	if err := a.db.DocumentPractice.CreateWorkflow(ctx, workflow, &action); err != nil {
		stored, getErr := a.db.DocumentPractice.GetWorkflow(ctx, workflow.ID)
		if getErr != nil {
			return documentdomain.Workflow{}, err
		}
		return stored, nil
	}
	return workflow, nil
}

func TestIgnitionRecordsTheActingAdminInTheHumanAudit(t *testing.T) {
	application := &auditRecordingDocumentationApplication{}
	server := newAuthTestServer(t, func(cfg *config.Config, dependencies *Dependencies, _ *postgres.Store) {
		dependencies.Documentation = application
	})
	application.db = server.db
	adminRegister := server.register(t, "alice", "alice-password")
	adminToken := server.login(t, "alice", "alice-password", adminRegister.TotpSecret)
	userRegister := server.register(t, "bob", "bob-password")
	userToken := server.login(t, "bob", "bob-password", userRegister.TotpSecret)

	recorder := server.do(t, http.MethodPost, "/api/documentation/practice", adminToken, nil)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("admin ignition = %d: %s", recorder.Code, recorder.Body.String())
	}
	if len(application.actors) != 1 || application.actors[0] == "" {
		t.Fatalf("ignition actors = %#v, want the admin identifier", application.actors)
	}
	rows, err := server.db.Audit.ListHumanActions(context.Background(), postgres.HumanActionFilter{Action: audit.ActionDocumentationPracticeStart, Limit: 10})
	if err != nil {
		t.Fatalf("list ignition audits: %v", err)
	}
	if len(rows) != 1 || rows[0].UserID != application.actors[0] || rows[0].TargetID != "document-workflow-01" {
		t.Fatalf("ignition audit rows = %#v", rows)
	}

	// A rejected non-admin ignition changes nothing and records nothing.
	recorder = server.do(t, http.MethodPost, "/api/documentation/practice", userToken, nil)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("non-admin ignition = %d, want 403", recorder.Code)
	}
	rows, err = server.db.Audit.ListHumanActions(context.Background(), postgres.HumanActionFilter{Action: audit.ActionDocumentationPracticeStart, Limit: 10})
	if err != nil || len(rows) != 1 {
		t.Fatalf("ignition audit rows after rejection = %#v, %v", rows, err)
	}
}

func TestAdminAuditEndpointFiltersAndPagesTheLedger(t *testing.T) {
	server := newAuthTestServerSimple(t, nil)
	adminRegister := server.register(t, "alice", "alice-password")
	adminToken := server.login(t, "alice", "alice-password", adminRegister.TotpSecret)
	userRegister := server.register(t, "bob", "bob-password")
	userToken := server.login(t, "bob", "bob-password", userRegister.TotpSecret)

	base := time.Now().UTC().Add(-time.Hour)
	for i := 0; i < 3; i++ {
		now := base.Add(time.Duration(i) * time.Minute)
		action := audit.HumanAction{
			ID:         audit.NewID(now),
			UserID:     "alice-id",
			Action:     audit.ActionUserTOTPReset,
			TargetType: audit.TargetUser,
			TargetID:   "u-victim",
			Detail:     json.RawMessage(`{"target_user_id":"u-victim"}`),
			CreatedAt:  now,
		}
		if err := server.db.Audit.RecordHumanAction(context.Background(), action); err != nil {
			t.Fatalf("seed audit row: %v", err)
		}
	}

	recorder := server.do(t, http.MethodGet, "/api/admin/audit", userToken, nil)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("non-admin audit list = %d, want 403", recorder.Code)
	}

	recorder = server.do(t, http.MethodGet, "/api/admin/audit?limit=2", adminToken, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("audit list = %d: %s", recorder.Code, recorder.Body.String())
	}
	var page api.AdminAuditPage
	if err := json.Unmarshal(recorder.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode audit page: %v", err)
	}
	if len(page.Items) != 2 || page.NextCursor == nil {
		t.Fatalf("first audit page = %#v", page)
	}

	recorder = server.do(t, http.MethodGet, "/api/admin/audit?limit=2&cursor="+*page.NextCursor, adminToken, nil)
	var secondPage api.AdminAuditPage
	if err := json.Unmarshal(recorder.Body.Bytes(), &secondPage); err != nil {
		t.Fatalf("decode second audit page: %v", err)
	}
	if recorder.Code != http.StatusOK || len(secondPage.Items) != 1 || secondPage.NextCursor != nil {
		t.Fatalf("second audit page = %d %#v", recorder.Code, secondPage)
	}

	recorder = server.do(t, http.MethodGet, "/api/admin/audit?action=user.totp.reset&user_id=missing", adminToken, nil)
	var filteredPage api.AdminAuditPage
	if err := json.Unmarshal(recorder.Body.Bytes(), &filteredPage); err != nil {
		t.Fatalf("decode filtered audit page: %v", err)
	}
	if len(filteredPage.Items) != 0 {
		t.Fatalf("filtered audit page = %#v, want no rows", filteredPage)
	}

	recorder = server.do(t, http.MethodGet, "/api/admin/audit?limit=0", adminToken, nil)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("audit list limit=0 = %d, want 400", recorder.Code)
	}
	recorder = server.do(t, http.MethodGet, "/api/admin/audit?cursor=bogus", adminToken, nil)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("audit list bogus cursor = %d, want 400", recorder.Code)
	}
}
