package postgres

var schemaRoadmapStatements = []string{
	`CREATE TABLE roadmap_revisions (
		id TEXT PRIMARY KEY,
		content_json JSONB NOT NULL,
		created_at TIMESTAMPTZ NOT NULL
	)`,
	`CREATE TABLE roadmap_current (
		singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
		revision_id TEXT REFERENCES roadmap_revisions(id) ON DELETE RESTRICT,
		updated_at TIMESTAMPTZ NOT NULL
	)`,
	`INSERT INTO roadmap_current (singleton, revision_id, updated_at)
		VALUES (TRUE, NULL, '1970-01-01T00:00:00Z'::timestamptz)`,
	`CREATE TABLE roadmap_maintenance_control (
		singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
		requested BOOLEAN NOT NULL DEFAULT FALSE,
		unrequested_challenge_count INTEGER NOT NULL DEFAULT 0 CHECK (unrequested_challenge_count >= 0),
		requested_at TIMESTAMPTZ
	)`,
	`INSERT INTO roadmap_maintenance_control (singleton, requested, unrequested_challenge_count)
		VALUES (TRUE, FALSE, 0)`,
	`CREATE TABLE roadmap_entries (
		challenge_id TEXT PRIMARY KEY,
		topic_id TEXT NOT NULL,
		topic_processed BOOLEAN NOT NULL,
		challenge_processed BOOLEAN NOT NULL,
		created_at TIMESTAMPTZ NOT NULL
	)`,
	`CREATE INDEX roadmap_entries_pending ON roadmap_entries(created_at, challenge_id)
		WHERE NOT topic_processed OR NOT challenge_processed`,
	`CREATE TABLE roadmap_workflows (
		id TEXT PRIMARY KEY,
		base_revision TEXT NOT NULL REFERENCES roadmap_revisions(id) ON DELETE RESTRICT,
		state TEXT NOT NULL CHECK (state IN ('Queued', 'Running', 'Publishing', 'Completed')),
		publish_attempt INTEGER NOT NULL DEFAULT 0 CHECK (publish_attempt >= 0),
		lease_owner TEXT NOT NULL DEFAULT '',
		lease_version INTEGER NOT NULL DEFAULT 0 CHECK (lease_version >= 0),
		lease_expires_at TIMESTAMPTZ,
		next_run_at TIMESTAMPTZ NOT NULL,
		last_error TEXT NOT NULL DEFAULT '',
		created_at TIMESTAMPTZ NOT NULL,
		updated_at TIMESTAMPTZ NOT NULL,
		CHECK ((lease_owner = '') = (lease_expires_at IS NULL))
	)`,
	`CREATE UNIQUE INDEX roadmap_workflows_active ON roadmap_workflows((TRUE))
		WHERE state IN ('Queued', 'Running', 'Publishing')`,
	`CREATE INDEX roadmap_workflows_recovery ON roadmap_workflows(state, next_run_at, lease_expires_at, created_at, id)`,
	`CREATE TABLE roadmap_workflow_entries (
		workflow_id TEXT NOT NULL REFERENCES roadmap_workflows(id) ON DELETE RESTRICT,
		challenge_id TEXT NOT NULL,
		topic_id TEXT NOT NULL,
		snapshot_order INTEGER NOT NULL CHECK (snapshot_order >= 0),
		topic_required BOOLEAN NOT NULL,
		challenge_required BOOLEAN NOT NULL,
		PRIMARY KEY (workflow_id, challenge_id),
		UNIQUE (workflow_id, snapshot_order)
	)`,
	`CREATE TABLE roadmap_tasks (
		id TEXT PRIMARY KEY,
		workflow_id TEXT NOT NULL REFERENCES roadmap_workflows(id) ON DELETE RESTRICT,
		entry_challenge_id TEXT NOT NULL,
		snapshot_order INTEGER NOT NULL CHECK (snapshot_order >= 0),
		kind TEXT NOT NULL CHECK (kind IN ('Topic', 'Challenge')),
		subject_id TEXT NOT NULL,
		subject_source_ref TEXT NOT NULL,
		subject_title TEXT NOT NULL,
		subject_content_revision TEXT NOT NULL DEFAULT '',
		state TEXT NOT NULL CHECK (state IN ('Pending', 'Running', 'Accepted', 'Failed')),
		round INTEGER NOT NULL DEFAULT 0 CHECK (round >= 0),
		planner_calls INTEGER NOT NULL DEFAULT 0 CHECK (planner_calls BETWEEN 0 AND 5),
		curriculum_calls INTEGER NOT NULL DEFAULT 0 CHECK (curriculum_calls BETWEEN 0 AND 5),
		sre_calls INTEGER NOT NULL DEFAULT 0 CHECK (sre_calls BETWEEN 0 AND 5),
		changeset_json JSONB,
		curriculum_review_json JSONB,
		sre_review_json JSONB,
		lease_owner TEXT NOT NULL DEFAULT '',
		lease_version INTEGER NOT NULL DEFAULT 0 CHECK (lease_version >= 0),
		lease_expires_at TIMESTAMPTZ,
		next_run_at TIMESTAMPTZ NOT NULL,
		last_error TEXT NOT NULL DEFAULT '',
		created_at TIMESTAMPTZ NOT NULL,
		updated_at TIMESTAMPTZ NOT NULL,
		UNIQUE (workflow_id, entry_challenge_id, kind),
		CHECK ((lease_owner = '') = (lease_expires_at IS NULL))
	)`,
	`CREATE INDEX roadmap_tasks_claim ON roadmap_tasks(workflow_id, state, next_run_at, lease_expires_at, snapshot_order, kind, id)`,
	`CREATE TABLE roadmap_edge_audits (
		id BIGSERIAL PRIMARY KEY,
		workflow_id TEXT NOT NULL REFERENCES roadmap_workflows(id) ON DELETE RESTRICT,
		task_id TEXT REFERENCES roadmap_tasks(id) ON DELETE RESTRICT,
		kind TEXT NOT NULL CHECK (kind IN ('Topic', 'Challenge')),
		edge_json JSONB NOT NULL,
		outcome TEXT NOT NULL,
		reason TEXT NOT NULL,
		created_at TIMESTAMPTZ NOT NULL
	)`,
	`CREATE INDEX roadmap_edge_audits_workflow ON roadmap_edge_audits(workflow_id, kind, id)`,
}
