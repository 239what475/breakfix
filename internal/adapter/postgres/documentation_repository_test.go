package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/domain/doclinks"
)

func documentationLinkFixture(key, title string, createdAt time.Time) doclinks.Link {
	return doclinks.Link{
		Key:       key,
		Title:     title,
		URL:       "https://kubernetes.io/docs/home/",
		Embed:     true,
		CreatedAt: createdAt,
		UpdatedAt: createdAt,
	}
}

func TestDocumentationLinkRepositoryOrdersByCreationAndRejectsDuplicateKeys(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	base := time.Date(2026, 9, 22, 9, 0, 0, 0, time.UTC)

	// Insert out of creation order on purpose: the list is the aggregation
	// page's render order, so it must come back oldest first.
	seed := []doclinks.Link{
		documentationLinkFixture("doc-third", "Probes", base.Add(2*time.Minute)),
		documentationLinkFixture("doc-first", "Home", base),
		documentationLinkFixture("doc-second", "Pods", base.Add(time.Minute)),
	}
	for _, link := range seed {
		if err := database.Documentation.CreateDocumentationLink(ctx, link); err != nil {
			t.Fatalf("create %s: %v", link.Key, err)
		}
	}
	links, err := database.Documentation.ListDocumentationLinks(ctx)
	if err != nil {
		t.Fatalf("list links: %v", err)
	}
	if len(links) != 3 {
		t.Fatalf("links = %d rows, want 3", len(links))
	}
	for i, want := range []string{"doc-first", "doc-second", "doc-third"} {
		if links[i].Key != want {
			t.Fatalf("links[%d].key = %q, want %q", i, links[i].Key, want)
		}
	}

	// The key is the primary key: a duplicate insert is a server-side fault.
	if err := database.Documentation.CreateDocumentationLink(ctx, documentationLinkFixture("doc-first", "Again", base)); err == nil {
		t.Fatalf("duplicate key was accepted")
	}

	got, err := database.Documentation.GetDocumentationLink(ctx, "doc-second")
	if err != nil {
		t.Fatalf("get link: %v", err)
	}
	if got.Title != "Pods" || got.URL != "https://kubernetes.io/docs/home/" || !got.Embed {
		t.Fatalf("got link = %#v", got)
	}
	if _, err := database.Documentation.GetDocumentationLink(ctx, "doc-missing"); !errors.Is(err, ErrDocumentationLinkNotFound) {
		t.Fatalf("missing link err = %v, want ErrDocumentationLinkNotFound", err)
	}
}

func TestDocumentationLinkRepositoryUpdateAndDelete(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	base := time.Date(2026, 9, 22, 9, 0, 0, 0, time.UTC)
	if err := database.Documentation.CreateDocumentationLink(ctx, documentationLinkFixture("doc-live", "Home", base)); err != nil {
		t.Fatalf("create link: %v", err)
	}

	// The key survives a full mutation of every mutable field.
	updated, err := database.Documentation.UpdateDocumentationLink(ctx, "doc-live", "Renamed", "http://mirror.internal/docs", false, base.Add(time.Hour))
	if err != nil {
		t.Fatalf("update link: %v", err)
	}
	if updated.Key != "doc-live" || updated.Title != "Renamed" || updated.URL != "http://mirror.internal/docs" || updated.Embed {
		t.Fatalf("updated link = %#v", updated)
	}
	if !updated.CreatedAt.Equal(base) || updated.UpdatedAt.Equal(updated.CreatedAt) {
		t.Fatalf("update must keep created_at and bump updated_at: %#v", updated)
	}

	if _, err := database.Documentation.UpdateDocumentationLink(ctx, "doc-missing", "X", "https://x.example", true, base); !errors.Is(err, ErrDocumentationLinkNotFound) {
		t.Fatalf("update missing err = %v, want ErrDocumentationLinkNotFound", err)
	}
	if _, err := database.Documentation.UpdateDocumentationLink(ctx, "doc-live", " ", "https://x.example", true, base); !errors.Is(err, doclinks.ErrLinkInvalid) {
		t.Fatalf("update with blank title err = %v, want ErrLinkInvalid", err)
	}

	deleted, err := database.Documentation.DeleteDocumentationLink(ctx, "doc-live")
	if err != nil || !deleted {
		t.Fatalf("delete link = %v, %v", deleted, err)
	}
	deleted, err = database.Documentation.DeleteDocumentationLink(ctx, "doc-live")
	if err != nil || deleted {
		t.Fatalf("second delete = %v, %v, want false, nil", deleted, err)
	}
}

func TestDocumentationLinkRepositoryEnforcesWriteRules(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	base := time.Date(2026, 9, 22, 9, 0, 0, 0, time.UTC)

	invalid := []doclinks.Link{
		{Key: "doc-a", Title: "Docs", URL: "ftp://x.example", CreatedAt: base, UpdatedAt: base},
		{Key: "doc-b", Title: "  ", URL: "https://x.example", CreatedAt: base, UpdatedAt: base},
		{Key: "", Title: "Docs", URL: "https://x.example", CreatedAt: base, UpdatedAt: base},
		{Key: "doc-c", Title: "Docs", URL: "https://x.example", CreatedAt: base, UpdatedAt: time.Time{}},
	}
	for _, link := range invalid {
		if err := database.Documentation.CreateDocumentationLink(ctx, link); err == nil {
			t.Fatalf("invalid link %#v was accepted", link)
		}
	}
	links, err := database.Documentation.ListDocumentationLinks(ctx)
	if err != nil || len(links) != 0 {
		t.Fatalf("links after rejected writes = %#v, %v", links, err)
	}
}
