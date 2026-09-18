package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	docsource "github.com/breakfix/breakfix/internal/adapter/documentation"
	documentdomain "github.com/breakfix/breakfix/internal/domain/documentpractice"
	"github.com/gin-gonic/gin"
)

type fakeDocumentationLibrary struct {
	page    docsource.DocumentPage
	pageErr error
	tree    []docsource.DocumentTreeChild
	asset   []byte
}

func (f fakeDocumentationLibrary) ReadDocumentPage(string) (docsource.DocumentPage, error) {
	return f.page, f.pageErr
}

func (f fakeDocumentationLibrary) ReadDocumentTree(string) ([]docsource.DocumentTreeChild, error) {
	return f.tree, nil
}

func (f fakeDocumentationLibrary) ReadDocumentAsset(string) ([]byte, string, error) {
	return f.asset, "image/svg+xml", nil
}

func (f fakeDocumentationLibrary) PinnedContext() documentdomain.DocumentContext {
	return documentdomain.DocumentContext{
		FormatVersion: documentdomain.FormatVersion, SourceID: "kubernetes",
		Repository: "https://github.com/kubernetes/website", Commit: strings.Repeat("a", 40),
		Version: "v1.34", Language: "en", License: "CC BY 4.0", PagePath: "docs/pods.md",
	}
}

func readEndpointContext(t *testing.T, target string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	request := httptest.NewRequest(http.MethodGet, target, nil)
	context.Request = request
	return context, recorder
}

func TestDocumentationReadEndpointsServeLibraryOutput(t *testing.T) {
	gin.SetMode(gin.TestMode)
	library := fakeDocumentationLibrary{
		page: docsource.DocumentPage{
			Path: "docs/concepts/workloads/pods/pod-lifecycle", PageKind: "content", Title: "Pod Lifecycle",
			Digest: "sha256:" + strings.Repeat("a", 64), Markdown: "# Pod Lifecycle\n\n![pod](../../../../images/docs/pod.svg)\n",
			Anchors: []docsource.DocumentAnchor{{ID: "pod-lifecycle", Level: 1, Title: "Pod Lifecycle"}},
			Assets:  []docsource.DocumentAsset{{Path: "images/docs/pod.svg", Digest: "sha256:" + strings.Repeat("b", 64)}},
		},
		tree:  []docsource.DocumentTreeChild{{Title: "Concepts", Path: "docs/concepts", HasChildren: true}},
		asset: []byte("<svg>pod</svg>\n"),
	}
	handler := &Handler{documentationLibrary: library}

	context, recorder := readEndpointContext(t, "/api/documentation/page?path=docs/concepts/workloads/pods/pod-lifecycle")
	handler.GetDocumentationPage(context)
	if recorder.Code != http.StatusOK {
		t.Fatalf("page status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, expected := range []string{`"title":"Pod Lifecycle"`, `"id":"pod-lifecycle"`, `/api/documentation/asset?path=images/docs/pod.svg`} {
		if !strings.Contains(body, expected) {
			t.Fatalf("page response missing %s: %s", expected, body)
		}
	}
	if strings.Contains(body, "../../../../images/docs/pod.svg") {
		t.Fatalf("page-relative asset reference was not rewritten: %s", body)
	}
	if etag := recorder.Header().Get("ETag"); etag != `"sha256:`+strings.Repeat("a", 64)+`"` {
		t.Fatalf("page ETag = %q", etag)
	}

	context, recorder = readEndpointContext(t, "/api/documentation/page?path=docs/concepts/workloads/pods/pod-lifecycle")
	context.Request.Header.Set("If-None-Match", `"sha256:`+strings.Repeat("a", 64)+`"`)
	handler.GetDocumentationPage(context)
	if recorder.Code != http.StatusNotModified {
		t.Fatalf("conditional page status = %d", recorder.Code)
	}

	context, recorder = readEndpointContext(t, "/api/documentation/tree")
	handler.GetDocumentationTree(context)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"has_children":true`) {
		t.Fatalf("tree status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	context, recorder = readEndpointContext(t, "/api/documentation/asset?path=images/docs/pod.svg")
	handler.GetDocumentationAsset(context)
	if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Type") != "image/svg+xml" || recorder.Body.String() != "<svg>pod</svg>\n" {
		t.Fatalf("asset status = %d, type = %q, body = %q", recorder.Code, recorder.Header().Get("Content-Type"), recorder.Body.String())
	}
	if cache := recorder.Header().Get("Cache-Control"); !strings.Contains(cache, "immutable") {
		t.Fatalf("asset cache header = %q", cache)
	}
}

func TestDocumentationReadEndpointsRejectUnknownLibraryContent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := &Handler{documentationLibrary: fakeDocumentationLibrary{pageErr: errUnknownForTest()}}

	context, recorder := readEndpointContext(t, "/api/documentation/page?path=docs/absent")
	handler.GetDocumentationPage(context)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("unknown page status = %d", recorder.Code)
	}

	unavailable := &Handler{}
	context, recorder = readEndpointContext(t, "/api/documentation/page?path=docs/x")
	handler2 := unavailable
	handler2.GetDocumentationPage(context)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured library status = %d", recorder.Code)
	}
}

func errUnknownForTest() error {
	return &unknownLibraryContentError{}
}

type unknownLibraryContentError struct{}

func (*unknownLibraryContentError) Error() string {
	return "documentation page is not part of the library"
}
