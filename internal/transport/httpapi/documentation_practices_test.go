package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/breakfix/breakfix/internal/adapter/postgres"
	documentdomain "github.com/breakfix/breakfix/internal/domain/documentpractice"
	"github.com/breakfix/breakfix/internal/domain/runnable"
	"github.com/gin-gonic/gin"
)

type fakePracticeReader struct {
	summaries  []postgres.PublishedPracticeSummary
	listErr    error
	revision   documentdomain.PracticeRevision
	getErr     error
	profile    runnable.RunnableRevision
	resolveErr error
}

func (f fakePracticeReader) ListPublishedPracticesForPage(context.Context, string, string, string, string) ([]postgres.PublishedPracticeSummary, error) {
	return f.summaries, f.listErr
}

func (f fakePracticeReader) GetPublishedPractice(context.Context, string) (documentdomain.PracticeRevision, error) {
	return f.revision, f.getErr
}

func (f fakePracticeReader) ResolveRunnableRevision(context.Context, string, string) (runnable.RunnableRevision, error) {
	return f.profile, f.resolveErr
}

func readerTestRevision(projection *documentdomain.ReaderProjection) documentdomain.PracticeRevision {
	return documentdomain.PracticeRevision{
		FormatVersion:       documentdomain.FormatVersion,
		ID:                  "practice-01",
		RunnableRevisionRef: runnable.RevisionReference{ID: "runnable-revision-01", Digest: "sha256:" + strings.Repeat("c", 64)},
		ReaderProjection:    projection,
	}
}

func TestListDocumentationPracticesConditionsOnTheSetDigest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := &Handler{
		documentationLibrary: fakeDocumentationLibrary{},
		documentationReader: fakePracticeReader{summaries: []postgres.PublishedPracticeSummary{
			{Anchor: "pod-lifetime", PracticeID: "practice-01", Title: "Pod lifecycle"},
		}},
	}

	context, recorder := readEndpointContext(t, "/api/documentation/practices?path=docs/concepts/workloads/pods/pod-lifecycle")
	handler.ListDocumentationPractices(context)
	if recorder.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, expected := range []string{`"anchor":"pod-lifetime"`, `"practice_id":"practice-01"`, `"title":"Pod lifecycle"`} {
		if !strings.Contains(body, expected) {
			t.Fatalf("practice list missing %s: %s", expected, body)
		}
	}
	etag := recorder.Header().Get("ETag")
	if etag == "" || etag == `""` {
		t.Fatalf("practice list ETag = %q", etag)
	}

	context, recorder = readEndpointContext(t, "/api/documentation/practices?path=docs/concepts/workloads/pods/pod-lifecycle")
	context.Request.Header.Set("If-None-Match", etag)
	handler.ListDocumentationPractices(context)
	if recorder.Code != http.StatusNotModified {
		t.Fatalf("conditional practice list status = %d", recorder.Code)
	}

	empty := &Handler{documentationLibrary: fakeDocumentationLibrary{}, documentationReader: fakePracticeReader{}}
	context, recorder = readEndpointContext(t, "/api/documentation/practices?path=docs/empty")
	empty.ListDocumentationPractices(context)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"practices":[]`) {
		t.Fatalf("empty practice list status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestGetDocumentationPracticeServesProjectionAndRuntimeSummary(t *testing.T) {
	gin.SetMode(gin.TestMode)
	projection := documentdomain.ReaderProjection{Title: "Pod lifecycle", Objective: "Observe Pod state", Boundary: "One Pod", Steps: []string{"Apply the Pod manifest"}, Observations: []string{"Pod reaches Running"}}
	handler := &Handler{
		documentationReader: fakePracticeReader{
			revision: readerTestRevision(&projection),
			profile:  runnable.RunnableRevision{Spec: runnable.RunnableSpec{RuntimeProfile: runnable.RuntimeProfile{Runtime: runnable.RuntimeK8s, BaseImage: "kindest/node"}}},
		},
	}

	context, recorder := readEndpointContext(t, "/api/documentation/practices/practice-01")
	handler.GetDocumentationPractice(context)
	if recorder.Code != http.StatusOK {
		t.Fatalf("detail status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, expected := range []string{`"title":"Pod lifecycle"`, `"objective":"Observe Pod state"`, `"boundary":"One Pod"`, `"steps":["Apply the Pod manifest"]`, `"observations":["Pod reaches Running"]`, `"runtime":{"base_image":"kindest/node","name":"k8s"}`} {
		if !strings.Contains(body, expected) {
			t.Fatalf("practice detail missing %s: %s", expected, body)
		}
	}
}

func TestGetDocumentationPracticeHidesUnknownAndLegacyRecords(t *testing.T) {
	gin.SetMode(gin.TestMode)
	// A legacy revision without a reader projection is reader-invisible and
	// reports exactly like an unknown ID.
	handler := &Handler{documentationReader: fakePracticeReader{getErr: postgres.ErrPublishedPracticeNotFound}}
	context, recorder := readEndpointContext(t, "/api/documentation/practices/practice-legacy")
	handler.GetDocumentationPractice(context)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("legacy practice status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	unconfigured := &Handler{}
	context, recorder = readEndpointContext(t, "/api/documentation/practices/practice-01")
	unconfigured.GetDocumentationPractice(context)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured reader status = %d", recorder.Code)
	}
}
