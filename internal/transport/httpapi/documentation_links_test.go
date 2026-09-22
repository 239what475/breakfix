package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/breakfix/breakfix/internal/adapter/postgres"
	"github.com/breakfix/breakfix/internal/domain/audit"
	api "github.com/breakfix/breakfix/internal/transport/httpapi/generated"
)

// The documentation link endpoints ride the real router so the optional-JWT
// public read and the RequireAdmin write fence are exercised as deployed.

func newDocumentationLinkTestServer(t *testing.T) (server *authTestServer, adminToken, userToken string) {
	t.Helper()
	server = newAuthTestServerSimple(t, nil)
	adminRegister := server.register(t, "alice", "alice-password")
	adminToken = server.login(t, "alice", "alice-password", adminRegister.TotpSecret)
	userRegister := server.register(t, "bob", "bob-password")
	userToken = server.login(t, "bob", "bob-password", userRegister.TotpSecret)
	return server, adminToken, userToken
}

func listDocumentationLinksViaAPI(t *testing.T, server *authTestServer, token string) (*http.Response, api.DocumentationLinkList) {
	t.Helper()
	recorder := server.do(t, http.MethodGet, "/api/documentation/links", token, nil)
	response := recorder.Result()
	var list api.DocumentationLinkList
	if err := json.NewDecoder(recorder.Body).Decode(&list); err != nil {
		t.Fatalf("decode documentation link list: %v", err)
	}
	return response, list
}

func TestDocumentationLinkListIsPublicAndStartsEmpty(t *testing.T) {
	server, _, _ := newDocumentationLinkTestServer(t)
	recorder, list := listDocumentationLinksViaAPI(t, server, "")
	if recorder.StatusCode != http.StatusOK {
		t.Fatalf("anonymous list = %d: %s", recorder.StatusCode, recorder.Body)
	}
	if len(list.Links) != 0 {
		t.Fatalf("fresh list = %#v, want empty", list.Links)
	}
}

func TestDocumentationLinkWritesRequireAdmin(t *testing.T) {
	server, adminToken, userToken := newDocumentationLinkTestServer(t)
	input := api.AdminDocumentationLinkInput{Title: "Kubernetes", Url: "https://kubernetes.io/docs", Embed: true}

	for _, attempt := range []struct {
		name  string
		token string
		want  int
	}{
		{"anonymous", "", http.StatusUnauthorized},
		{"member", userToken, http.StatusForbidden},
	} {
		recorder := server.do(t, http.MethodPost, "/api/admin/documentation/links", attempt.token, input)
		if recorder.Code != attempt.want {
			t.Fatalf("%s create = %d: %s, want %d", attempt.name, recorder.Code, recorder.Body.String(), attempt.want)
		}
		recorder = server.do(t, http.MethodPatch, "/api/admin/documentation/links/doc-x", attempt.token, input)
		if recorder.Code != attempt.want {
			t.Fatalf("%s update = %d: %s, want %d", attempt.name, recorder.Code, recorder.Body.String(), attempt.want)
		}
		recorder = server.do(t, http.MethodDelete, "/api/admin/documentation/links/doc-x", attempt.token, nil)
		if recorder.Code != attempt.want {
			t.Fatalf("%s delete = %d: %s, want %d", attempt.name, recorder.Code, recorder.Body.String(), attempt.want)
		}
	}

	// The admin write path works and the ledger records the verb.
	recorder := server.do(t, http.MethodPost, "/api/admin/documentation/links", adminToken, input)
	if recorder.Code != http.StatusOK {
		t.Fatalf("admin create = %d: %s", recorder.Code, recorder.Body.String())
	}
	var created api.DocumentationLink
	if err := json.Unmarshal(recorder.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(created.Key, "doc-") || created.Title != "Kubernetes" || created.Url != "https://kubernetes.io/docs" || !created.Embed {
		t.Fatalf("created link = %#v", created)
	}

	_, list := listDocumentationLinksViaAPI(t, server, "")
	if len(list.Links) != 1 || list.Links[0].Key != created.Key {
		t.Fatalf("list after create = %#v, want the created link", list.Links)
	}

	recorder = server.do(t, http.MethodDelete, "/api/admin/documentation/links/"+created.Key, adminToken, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("admin delete = %d: %s", recorder.Code, recorder.Body.String())
	}
	_, list = listDocumentationLinksViaAPI(t, server, "")
	if len(list.Links) != 0 {
		t.Fatalf("list after delete = %#v, want empty", list.Links)
	}

	actions, err := server.db.Audit.ListHumanActions(context.Background(), postgres.HumanActionFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(actions) != 2 {
		t.Fatalf("audit rows = %d, want create and delete", len(actions))
	}
	if actions[0].Action != audit.ActionDocumentationLinkWrite || actions[0].TargetID != created.Key || actions[0].UserID == "" {
		t.Fatalf("newest audit row = %#v", actions[0])
	}
	details := map[string]string{}
	for _, action := range actions {
		var detail struct {
			Op string `json:"op"`
		}
		if err := json.Unmarshal(action.Detail, &detail); err != nil {
			t.Fatal(err)
		}
		details[detail.Op] = action.TargetID
	}
	if details["create"] != created.Key || details["delete"] != created.Key {
		t.Fatalf("audit ops = %#v, want create and delete on the same key", details)
	}
}

func TestDocumentationLinkValidationAndMissingKeys(t *testing.T) {
	server, adminToken, _ := newDocumentationLinkTestServer(t)

	for name, input := range map[string]api.AdminDocumentationLinkInput{
		"blank title": {Title: "   ", Url: "https://kubernetes.io", Embed: true},
		"ftp url":     {Title: "Docs", Url: "ftp://kubernetes.io", Embed: true},
		"scheme-less": {Title: "Docs", Url: "kubernetes.io/docs", Embed: false},
		"hostless":    {Title: "Docs", Url: "https://", Embed: true},
		"garbage url": {Title: "Docs", Url: "://", Embed: true},
	} {
		recorder := server.do(t, http.MethodPost, "/api/admin/documentation/links", adminToken, input)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s create = %d: %s, want 400", name, recorder.Code, recorder.Body.String())
		}
	}

	input := api.AdminDocumentationLinkInput{Title: "Kubernetes", Url: "https://kubernetes.io/docs", Embed: true}
	recorder := server.do(t, http.MethodPatch, "/api/admin/documentation/links/doc-missing", adminToken, input)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("patch missing key = %d: %s, want 404", recorder.Code, recorder.Body.String())
	}
	recorder = server.do(t, http.MethodDelete, "/api/admin/documentation/links/doc-missing", adminToken, nil)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("delete missing key = %d: %s, want 404", recorder.Code, recorder.Body.String())
	}
	recorder = server.do(t, http.MethodPatch, "/api/admin/documentation/links/doc-missing", adminToken, nil)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("patch without body = %d: %s, want 400", recorder.Code, recorder.Body.String())
	}
}
