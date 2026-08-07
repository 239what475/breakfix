package postgres

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/breakfix/breakfix/internal/content/challenge"
	catalogdomain "github.com/breakfix/breakfix/internal/domain/catalog"
	"github.com/breakfix/breakfix/internal/domain/generation"
)

// PublicationRepository derives the exact Server data PVC retention set from
// durable publication facts and in-flight intents across the two publishers.
type PublicationRepository struct {
	conn *Conn
}

func (d *PublicationRepository) RetainedMaterializationPaths(ctx context.Context) ([]string, error) {
	rows, err := d.conn.QueryContext(ctx, `
		SELECT materialized_path, source_slug, id, 'challenge-revision'
		FROM challenge_revisions
		UNION ALL
		SELECT candidate.publication ->> 'target_path', candidate.publication ->> 'source_slug',
			candidate.publication ->> 'challenge_revision_id', 'generation-publication'
		FROM generation_workflows workflow
		JOIN candidate_revisions candidate ON candidate.id = workflow.candidate_revision_id
		WHERE workflow.state = ? AND candidate.published_at IS NULL AND candidate.publication IS NOT NULL
		UNION ALL
		SELECT CONCAT(commit.source_slug, '/', commit.challenge_revision_id), commit.source_slug, commit.challenge_revision_id, 'catalog-commit'
		FROM catalog_release_entry_commits commit
		JOIN catalog_releases release ON release.id = commit.release_id
		WHERE release.state = ? AND commit.state IN (?, ?, ?)`,
		generation.StateChallengePublishing, catalogdomain.ReleaseCommitting,
		catalogdomain.CommitPrepared, catalogdomain.CommitArtifactPublished, catalogdomain.CommitMaterialized)
	if err != nil {
		return nil, fmt.Errorf("query retained materializations: %w", err)
	}
	defer func() { _ = rows.Close() }()

	retained := make(map[string]struct{})
	for rows.Next() {
		var path, sourceSlug, revisionID, source string
		if err := rows.Scan(&path, &sourceSlug, &revisionID, &source); err != nil {
			return nil, fmt.Errorf("scan retained materialization: %w", err)
		}
		path = strings.TrimSpace(path)
		sourceSlug = strings.TrimSpace(sourceSlug)
		revisionID = strings.TrimSpace(revisionID)
		if err := challenge.ValidateMaterializedPath(path, sourceSlug, revisionID); err != nil {
			return nil, fmt.Errorf("%s has invalid materialized path: %w", source, err)
		}
		retained[path] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate retained materializations: %w", err)
	}
	paths := make([]string, 0, len(retained))
	for path := range retained {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths, nil
}
