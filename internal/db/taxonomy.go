package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/agentruntime"
	"github.com/breakfix/breakfix/internal/taxonomy"
	"github.com/breakfix/breakfix/internal/worklist"
)

var ErrTaxonomyMappingNotFound = errors.New("taxonomy mapping not found")

func (d *DB) EnqueueTaxonomyMapping(ctx context.Context, mapping taxonomy.TaxonomyMapping) (*taxonomy.TaxonomyMapping, bool, error) {
	if strings.TrimSpace(mapping.ID) == "" || strings.TrimSpace(mapping.ChallengeID) == "" || strings.TrimSpace(mapping.ChallengeRevision) == "" {
		return nil, false, errors.New("taxonomy mapping requires id and challenge revision")
	}
	if mapping.State == "" {
		mapping.State = taxonomy.MappingPending
	}
	if mapping.State != taxonomy.MappingPending || mapping.ActiveStage != "" || mapping.ActiveRunID != "" || mapping.Round < 0 {
		return nil, false, errors.New("new taxonomy mapping must be inactive and pending")
	}
	now := time.Now().UTC()
	if mapping.CreatedAt.IsZero() {
		mapping.CreatedAt = now
	}
	mapping.UpdatedAt = now
	result, err := d.conn.ExecContext(ctx, `INSERT INTO taxonomy_mappings
		(id, challenge_id, challenge_revision, base_revision, state, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(challenge_id, challenge_revision) DO NOTHING`,
		mapping.ID, mapping.ChallengeID, mapping.ChallengeRevision, mapping.BaseRevision,
		mapping.State, mapping.CreatedAt, mapping.UpdatedAt)
	if err != nil {
		return nil, false, fmt.Errorf("enqueue taxonomy mapping: %w", err)
	}
	item, err := d.GetTaxonomyMappingByChallenge(ctx, mapping.ChallengeID, mapping.ChallengeRevision)
	if err != nil {
		return nil, false, err
	}
	created, err := result.RowsAffected()
	if err != nil {
		return nil, false, fmt.Errorf("read taxonomy mapping insert result: %w", err)
	}
	return item, created == 1, nil
}

func (d *DB) GetTaxonomyMapping(ctx context.Context, id string) (*taxonomy.TaxonomyMapping, error) {
	if strings.TrimSpace(id) == "" {
		return nil, errors.New("taxonomy mapping id is required")
	}
	return d.readTaxonomyMapping(ctx, `SELECT `+taxonomyMappingColumns+` FROM taxonomy_mappings WHERE id = ?`, id)
}

func (d *DB) GetTaxonomyMappingByChallenge(ctx context.Context, challengeID, revision string) (*taxonomy.TaxonomyMapping, error) {
	return d.readTaxonomyMapping(ctx, `SELECT `+taxonomyMappingColumns+`
		FROM taxonomy_mappings WHERE challenge_id = ? AND challenge_revision = ?`, challengeID, revision)
}

func (d *DB) ListTaxonomyMappings(ctx context.Context) ([]taxonomy.TaxonomyMapping, error) {
	rows, err := d.conn.QueryContext(ctx, `SELECT `+taxonomyMappingColumns+` FROM taxonomy_mappings ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("list taxonomy mappings: %w", err)
	}
	defer func() { _ = rows.Close() }()
	items := make([]taxonomy.TaxonomyMapping, 0)
	for rows.Next() {
		item, err := scanTaxonomyMapping(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, *item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate taxonomy mappings: %w", err)
	}
	return items, nil
}

// ListUnpublishedTaxonomyMappings returns unfinished mappings so the catalog
// scanner can cancel work whose filesystem-backed challenge revision vanished.
func (d *DB) ListUnpublishedTaxonomyMappings(ctx context.Context) ([]taxonomy.TaxonomyMapping, error) {
	rows, err := d.conn.QueryContext(ctx, `SELECT `+taxonomyMappingColumns+` FROM taxonomy_mappings
		WHERE state IN (?, ?, ?) ORDER BY created_at, id`,
		taxonomy.MappingPending, taxonomy.MappingReadyPublish, taxonomy.MappingFailed)
	if err != nil {
		return nil, fmt.Errorf("list unpublished taxonomy mappings: %w", err)
	}
	defer func() { _ = rows.Close() }()
	mappings := make([]taxonomy.TaxonomyMapping, 0)
	for rows.Next() {
		mapping, err := scanTaxonomyMapping(rows)
		if err != nil {
			return nil, err
		}
		mappings = append(mappings, *mapping)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate unpublished taxonomy mappings: %w", err)
	}
	return mappings, nil
}

// NextTaxonomyMapping returns only a mapping for which Server can take an
// immediate domain step. Running AgentRuns remain owned by their WorkItems and
// are excluded until they become terminal.
func (d *DB) NextTaxonomyMapping(ctx context.Context) (*taxonomy.TaxonomyMapping, error) {
	item, err := scanTaxonomyMapping(d.conn.QueryRowContext(ctx, `SELECT `+taxonomyMappingColumns+`
		FROM taxonomy_mappings m
		WHERE m.state = ? OR (
			m.state = ? AND (
				m.active_run_id = '' OR EXISTS (
					SELECT 1 FROM agent_runs r WHERE r.id = m.active_run_id AND r.status IN (?, ?, ?)
				)
			)
		)
		ORDER BY m.updated_at, m.created_at, m.id LIMIT 1`,
		taxonomy.MappingReadyPublish, taxonomy.MappingPending,
		agentruntime.RunSucceeded, agentruntime.RunFailed, agentruntime.RunCancelled))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("select actionable taxonomy mapping: %w", err)
	}
	return item, nil
}

func (d *DB) SaveTaxonomyMapping(ctx context.Context, mapping taxonomy.TaxonomyMapping) error {
	if strings.TrimSpace(mapping.ID) == "" || !validTaxonomyMappingState(mapping.State) || mapping.Round < 0 {
		return errors.New("valid taxonomy mapping is required")
	}
	if (mapping.ActiveStage == "") != (mapping.ActiveRunID == "") {
		return errors.New("taxonomy active stage and run id must be set together")
	}
	if mapping.ActiveStage != "" {
		if _, err := taxonomy.PurposeForStage(mapping.ActiveStage); err != nil {
			return err
		}
	}
	candidate, curriculum, sre, err := encodeTaxonomyMapping(mapping)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	result, err := d.conn.ExecContext(ctx, `UPDATE taxonomy_mappings SET
		base_revision = ?, active_stage = ?, active_run_id = ?, candidate_json = ?::jsonb,
		curriculum_review_json = ?::jsonb, sre_review_json = ?::jsonb, round = ?, state = ?,
		published_revision = ?, last_error = ?, updated_at = ? WHERE id = ?`,
		mapping.BaseRevision, mapping.ActiveStage, mapping.ActiveRunID, candidate, curriculum, sre,
		mapping.Round, mapping.State, mapping.PublishedRevision, mapping.LastError, now, mapping.ID)
	if err != nil {
		return fmt.Errorf("save taxonomy mapping: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return ErrTaxonomyMappingNotFound
	}
	return nil
}

func (d *DB) CancelTaxonomyMapping(ctx context.Context, mappingID, reason string) error {
	if strings.TrimSpace(mappingID) == "" || strings.TrimSpace(reason) == "" {
		return errors.New("taxonomy mapping and cancellation reason are required")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin cancel taxonomy mapping: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var activeRunID string
	err = tx.QueryRowContext(ctx, `SELECT active_run_id FROM taxonomy_mappings WHERE id = ? FOR UPDATE`, mappingID).Scan(&activeRunID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrTaxonomyMappingNotFound
	}
	if err != nil {
		return fmt.Errorf("lock taxonomy mapping for cancellation: %w", err)
	}
	now := time.Now().UTC()
	if activeRunID != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE agent_runs SET status = ?, completed_at = ?, updated_at = ?
			WHERE id = ? AND status IN (?, ?)`, agentruntime.RunCancelled, now, now, activeRunID,
			agentruntime.RunPending, agentruntime.RunRunning); err != nil {
			return fmt.Errorf("cancel taxonomy agent run: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE work_items SET state = ?, lease_owner = '', lease_expires_at = NULL,
			error_code = 'taxonomy_mapping_cancelled', error_summary = ?, updated_at = ?
			WHERE kind = ? AND subject_type = ? AND subject_id = ? AND state IN (?, ?)`,
			worklist.StateCancelled, reason, now, worklist.KindAgent, worklist.SubjectAgentRun,
			activeRunID, worklist.StatePending, worklist.StateRunning); err != nil {
			return fmt.Errorf("cancel taxonomy agent work item: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE taxonomy_mappings SET state = ?, active_stage = '', active_run_id = '',
		last_error = ?, updated_at = ? WHERE id = ?`, taxonomy.MappingCancelled, reason, now, mappingID); err != nil {
		return fmt.Errorf("cancel taxonomy mapping: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit taxonomy mapping cancellation: %w", err)
	}
	return nil
}

func (d *DB) ScheduleTaxonomyRun(ctx context.Context, mapping taxonomy.TaxonomyMapping, run agentruntime.CreateRun) (*agentruntime.Run, error) {
	if strings.TrimSpace(mapping.ID) == "" || mapping.ActiveStage != "" || mapping.ActiveRunID != "" {
		return nil, errors.New("inactive taxonomy mapping is required to schedule a run")
	}
	if err := agentruntime.ValidateCreateRun(run); err != nil {
		return nil, err
	}
	input, err := taxonomy.DecodeRunInput(run.Input)
	if err != nil {
		return nil, fmt.Errorf("decode taxonomy run input: %w", err)
	}
	purpose, err := taxonomy.PurposeForStage(input.Stage)
	if err != nil {
		return nil, err
	}
	if input.WorkID != mapping.ID || input.Round != mapping.Round || run.SessionID != "" || run.Purpose != purpose || run.OwnerKind != "taxonomy-mapping" || run.OwnerRef != mapping.ID {
		return nil, errors.New("taxonomy agent run does not match its mapping")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin schedule taxonomy run: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var activeStage taxonomy.WorkStage
	var activeRunID string
	var round int
	var state taxonomy.MappingState
	if err := tx.QueryRowContext(ctx, `SELECT active_stage, active_run_id, round, state FROM taxonomy_mappings WHERE id = ? FOR UPDATE`, mapping.ID).
		Scan(&activeStage, &activeRunID, &round, &state); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrTaxonomyMappingNotFound
		}
		return nil, fmt.Errorf("lock taxonomy mapping: %w", err)
	}
	if activeStage != "" || activeRunID != "" || round != mapping.Round || state != taxonomy.MappingPending {
		return nil, ErrTaxonomyMappingNotFound
	}
	now := time.Now().UTC()
	created, err := createRunTx(ctx, tx, run, now)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE taxonomy_mappings SET base_revision = ?, active_stage = ?,
		active_run_id = ?, updated_at = ? WHERE id = ?`, mapping.BaseRevision, input.Stage, created.ID, now, mapping.ID); err != nil {
		return nil, fmt.Errorf("bind taxonomy mapping to agent run: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit taxonomy run scheduling: %w", err)
	}
	return created, nil
}

func (d *DB) FinalizeTaxonomyMapperRun(ctx context.Context, claim agentruntime.Claim, mappingID, baseRevision string, candidate taxonomy.ChangeSet) error {
	encoded, err := marshalOptional(&candidate)
	if err != nil {
		return err
	}
	if strings.TrimSpace(baseRevision) == "" && candidate.Empty() {
		return errors.New("taxonomy mapper candidate is empty")
	}
	return d.finalizeTaxonomyRun(ctx, claim, mappingID, taxonomy.WorkStageMapper, func(tx *Tx, now time.Time) error {
		result, err := tx.ExecContext(ctx, `UPDATE taxonomy_mappings SET base_revision = ?, candidate_json = ?::jsonb,
			curriculum_review_json = NULL, sre_review_json = NULL, active_stage = '', active_run_id = '',
			state = ?, last_error = '', updated_at = ? WHERE id = ? AND active_stage = ? AND active_run_id = ?`,
			baseRevision, encoded, taxonomy.MappingPending, now, mappingID, taxonomy.WorkStageMapper, claim.Run.ID)
		if err != nil {
			return fmt.Errorf("persist taxonomy mapper candidate: %w", err)
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return ErrTaxonomyMappingNotFound
		}
		return nil
	})
}

func (d *DB) FinalizeTaxonomyReviewRun(ctx context.Context, claim agentruntime.Claim, mappingID string, curriculum, sre taxonomy.Review) error {
	curriculumJSON, err := marshalOptional(&curriculum)
	if err != nil {
		return err
	}
	sreJSON, err := marshalOptional(&sre)
	if err != nil {
		return err
	}
	state := taxonomy.MappingPending
	if curriculum.Decision == taxonomy.ReviewApprove && sre.Decision == taxonomy.ReviewApprove {
		state = taxonomy.MappingReadyPublish
	}
	return d.finalizeTaxonomyRun(ctx, claim, mappingID, taxonomy.WorkStageReview, func(tx *Tx, now time.Time) error {
		result, err := tx.ExecContext(ctx, `UPDATE taxonomy_mappings SET curriculum_review_json = ?::jsonb,
			sre_review_json = ?::jsonb, round = round + 1, active_stage = '', active_run_id = '',
			state = ?, last_error = '', updated_at = ? WHERE id = ? AND active_stage = ? AND active_run_id = ?`,
			curriculumJSON, sreJSON, state, now, mappingID, taxonomy.WorkStageReview, claim.Run.ID)
		if err != nil {
			return fmt.Errorf("persist taxonomy reviewer pair: %w", err)
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return ErrTaxonomyMappingNotFound
		}
		return nil
	})
}

func (d *DB) finalizeTaxonomyRun(ctx context.Context, claim agentruntime.Claim, mappingID string, stage taxonomy.WorkStage, apply func(*Tx, time.Time) error) error {
	if !claim.Valid() || strings.TrimSpace(mappingID) == "" {
		return errors.New("valid taxonomy claim and mapping id are required")
	}
	purpose, err := taxonomy.PurposeForStage(stage)
	if err != nil {
		return err
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin finalize taxonomy run: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UTC()
	if _, err := lockAgentClaim(ctx, tx, claim, now); err != nil {
		return err
	}
	if claim.Run.Purpose != purpose || claim.Run.OwnerKind != "taxonomy-mapping" || claim.Run.OwnerRef != mappingID {
		return agentruntime.ErrLeaseLost
	}
	if err := apply(tx, now); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE agent_runs SET status = ?, completed_at = ?, updated_at = ?
		WHERE id = ? AND status = ?`, agentruntime.RunSucceeded, now, now, claim.Run.ID, agentruntime.RunRunning)
	if err != nil {
		return fmt.Errorf("complete taxonomy agent run: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return agentruntime.ErrLeaseLost
	}
	if err := completeAgentWorkItemTx(ctx, tx, claim, worklist.StateSucceeded, "", "", now); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit taxonomy agent finalization: %w", err)
	}
	return nil
}

const taxonomyMappingColumns = `id, challenge_id, challenge_revision, base_revision, active_stage, active_run_id,
	COALESCE(candidate_json::text, ''), COALESCE(curriculum_review_json::text, ''), COALESCE(sre_review_json::text, ''),
	round, state, published_revision, last_error, created_at, updated_at`

type taxonomyMappingScanner interface {
	Scan(...any) error
}

func (d *DB) readTaxonomyMapping(ctx context.Context, query string, args ...any) (*taxonomy.TaxonomyMapping, error) {
	item, err := scanTaxonomyMapping(d.conn.QueryRowContext(ctx, query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrTaxonomyMappingNotFound
	}
	return item, err
}

func scanTaxonomyMapping(scanner taxonomyMappingScanner) (*taxonomy.TaxonomyMapping, error) {
	var item taxonomy.TaxonomyMapping
	var candidate, curriculum, sre string
	if err := scanner.Scan(&item.ID, &item.ChallengeID, &item.ChallengeRevision, &item.BaseRevision,
		&item.ActiveStage, &item.ActiveRunID, &candidate, &curriculum, &sre, &item.Round,
		&item.State, &item.PublishedRevision, &item.LastError, &item.CreatedAt, &item.UpdatedAt); err != nil {
		return nil, err
	}
	if err := unmarshalOptional(candidate, &item.Candidate); err != nil {
		return nil, fmt.Errorf("decode taxonomy candidate for %q: %w", item.ID, err)
	}
	if err := unmarshalOptional(curriculum, &item.CurriculumReview); err != nil {
		return nil, fmt.Errorf("decode curriculum review for %q: %w", item.ID, err)
	}
	if err := unmarshalOptional(sre, &item.SREReview); err != nil {
		return nil, fmt.Errorf("decode SRE review for %q: %w", item.ID, err)
	}
	item.CreatedAt = item.CreatedAt.UTC()
	item.UpdatedAt = item.UpdatedAt.UTC()
	return &item, nil
}

func validTaxonomyMappingState(value taxonomy.MappingState) bool {
	switch value {
	case taxonomy.MappingPending, taxonomy.MappingReadyPublish, taxonomy.MappingPublished,
		taxonomy.MappingFailed, taxonomy.MappingCancelled:
		return true
	default:
		return false
	}
}

func encodeTaxonomyMapping(mapping taxonomy.TaxonomyMapping) (string, string, string, error) {
	candidate, err := marshalOptional(mapping.Candidate)
	if err != nil {
		return "", "", "", err
	}
	curriculum, err := marshalOptional(mapping.CurriculumReview)
	if err != nil {
		return "", "", "", err
	}
	sre, err := marshalOptional(mapping.SREReview)
	if err != nil {
		return "", "", "", err
	}
	return candidate, curriculum, sre, nil
}

func marshalOptional(value any) (string, error) {
	if value == nil {
		return "null", nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func unmarshalOptional(data string, target any) error {
	if strings.TrimSpace(data) == "" || strings.TrimSpace(data) == "null" {
		return nil
	}
	return json.Unmarshal([]byte(data), target)
}
