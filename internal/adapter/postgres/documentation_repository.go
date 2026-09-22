package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/breakfix/breakfix/internal/domain/doclinks"
)

// ErrDocumentationLinkNotFound reports a missing key without exposing SQL
// internals to the transport layer.
var ErrDocumentationLinkNotFound = errors.New("documentation link not found")

// ListDocumentationLinks returns every link in creation order, the order the
// aggregation page renders.
func (d *DocumentationLinkRepository) ListDocumentationLinks(ctx context.Context) ([]doclinks.Link, error) {
	rows, err := d.conn.QueryContext(ctx, `SELECT key, title, url, embed, created_at, updated_at FROM documentation_links ORDER BY created_at, key`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	links := make([]doclinks.Link, 0)
	for rows.Next() {
		link, err := scanDocumentationLink(rows.Scan)
		if err != nil {
			return nil, err
		}
		links = append(links, link)
	}
	return links, rows.Err()
}

// GetDocumentationLink reads one link by key.
func (d *DocumentationLinkRepository) GetDocumentationLink(ctx context.Context, key string) (doclinks.Link, error) {
	row := d.conn.QueryRowContext(ctx, `SELECT key, title, url, embed, created_at, updated_at FROM documentation_links WHERE key = ?`, key)
	link, err := scanDocumentationLink(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return doclinks.Link{}, ErrDocumentationLinkNotFound
	}
	return link, err
}

// CreateDocumentationLink inserts one link. The key must have been generated
// by doclinks.NewKey; a duplicate key is a server-side fault, not a
// user-facing collision.
func (d *DocumentationLinkRepository) CreateDocumentationLink(ctx context.Context, link doclinks.Link) error {
	if err := link.Validate(); err != nil {
		return err
	}
	if link.Key == "" || link.CreatedAt.IsZero() || link.UpdatedAt.IsZero() {
		return errors.New("documentation link identity is incomplete")
	}
	_, err := d.conn.ExecContext(ctx, `INSERT INTO documentation_links (key, title, url, embed, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		link.Key, link.Title, link.URL, link.Embed, link.CreatedAt.UTC(), link.UpdatedAt.UTC())
	return err
}

// UpdateDocumentationLink replaces the mutable fields and bumps updated_at.
// The key is the immutable identity, so a rename never invalidates deep links.
func (d *DocumentationLinkRepository) UpdateDocumentationLink(ctx context.Context, key string, title, url string, embed bool, now time.Time) (doclinks.Link, error) {
	link := doclinks.Link{Key: key, Title: title, URL: url, Embed: embed}
	if err := link.Validate(); err != nil {
		return doclinks.Link{}, err
	}
	if now.IsZero() {
		return doclinks.Link{}, errors.New("documentation link update time is required")
	}
	row := d.conn.QueryRowContext(ctx, `UPDATE documentation_links SET title = ?, url = ?, embed = ?, updated_at = ? WHERE key = ? RETURNING key, title, url, embed, created_at, updated_at`,
		title, url, embed, now.UTC(), key)
	updated, err := scanDocumentationLink(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return doclinks.Link{}, ErrDocumentationLinkNotFound
	}
	return updated, err
}

// DeleteDocumentationLink removes one link and reports whether a row was
// actually deleted.
func (d *DocumentationLinkRepository) DeleteDocumentationLink(ctx context.Context, key string) (bool, error) {
	result, err := d.conn.ExecContext(ctx, `DELETE FROM documentation_links WHERE key = ?`, key)
	if err != nil {
		return false, err
	}
	deleted, err := result.RowsAffected()
	return deleted > 0, err
}

func scanDocumentationLink(scan func(dest ...any) error) (doclinks.Link, error) {
	var link doclinks.Link
	if err := scan(&link.Key, &link.Title, &link.URL, &link.Embed, &link.CreatedAt, &link.UpdatedAt); err != nil {
		return doclinks.Link{}, err
	}
	return link, nil
}
