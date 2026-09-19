package postgres

// Document practice records are append-only. No update path is exposed for
// these tables; a revised plan or candidate receives a new artifact digest.
// Distinct ledger entries may share a digest when a restarted workflow
// re-proposes byte-identical content under a new attempt-scoped identifier.
var schemaDocumentPracticeStatements = []string{
	`CREATE TABLE document_artifact_ledger (
		id TEXT PRIMARY KEY,
		workflow_id TEXT NOT NULL,
		kind TEXT NOT NULL,
		parent_id TEXT NOT NULL DEFAULT '',
		content_revision TEXT NOT NULL,
		digest TEXT NOT NULL,
		schema_version TEXT NOT NULL,
		owner_role TEXT NOT NULL,
		policy_version TEXT NOT NULL DEFAULT '',
		payload JSONB NOT NULL,
		created_at TIMESTAMPTZ NOT NULL
	)`,
	`CREATE INDEX document_artifact_ledger_workflow ON document_artifact_ledger(workflow_id, created_at, id)`,
	`CREATE TABLE document_workflows (
		id TEXT PRIMARY KEY,
		state TEXT NOT NULL CHECK (state IN ('Planning','PlanReviewing','Generating','ArtifactReviewing','MaterializingArtifact','Verifying','VerificationReviewing','Publishing','Published','NoPractice','Rejected','Failed')),
		state_version BIGINT NOT NULL CHECK (state_version >= 1),
		revision BIGINT NOT NULL CHECK (revision >= 1),
		max_revisions BIGINT NOT NULL CHECK (max_revisions >= 1),
		source_id TEXT NOT NULL DEFAULT '',
		commit TEXT NOT NULL DEFAULT '',
		language TEXT NOT NULL DEFAULT '',
		page_path TEXT NOT NULL DEFAULT '',
		anchor TEXT NOT NULL DEFAULT '',
		updated_at TIMESTAMPTZ NOT NULL
	)`,
	`CREATE INDEX document_workflows_page ON document_workflows(source_id, commit, language, page_path, anchor)`,
	`CREATE TABLE document_runnable_actions (
		action_key TEXT PRIMARY KEY,
		workflow_id TEXT NOT NULL REFERENCES document_workflows(id) ON DELETE RESTRICT,
		content_kind TEXT NOT NULL,
		content_id TEXT NOT NULL,
		content_revision TEXT NOT NULL,
		spec_digest TEXT NOT NULL,
		phase TEXT NOT NULL CHECK (phase IN ('materialize-artifact', 'verify')),
		state_version BIGINT NOT NULL CHECK (state_version >= 1),
		created_at TIMESTAMPTZ NOT NULL,
		reconciled_at TIMESTAMPTZ,
		UNIQUE(workflow_id, phase, state_version)
	)`,
	`CREATE INDEX document_runnable_actions_pending ON document_runnable_actions(reconciled_at, created_at) WHERE reconciled_at IS NULL`,
	`CREATE TABLE document_agent_audits (
		run_id TEXT PRIMARY KEY,
		workflow_id TEXT NOT NULL REFERENCES document_workflows(id) ON DELETE RESTRICT,
		role TEXT NOT NULL,
		model TEXT NOT NULL,
		prompt_version TEXT NOT NULL,
		tool_version TEXT NOT NULL,
		policy_version TEXT NOT NULL,
		input_digest TEXT NOT NULL,
		output_digest TEXT NOT NULL,
		created_at TIMESTAMPTZ NOT NULL
	)`,
	`CREATE INDEX document_agent_audits_workflow ON document_agent_audits(workflow_id, created_at, run_id)`,
	`CREATE TABLE document_publication_manifests (
		id TEXT PRIMARY KEY,
		workflow_id TEXT NOT NULL REFERENCES document_workflows(id) ON DELETE RESTRICT,
		manifest JSONB NOT NULL,
		manifest_digest TEXT NOT NULL UNIQUE,
		created_at TIMESTAMPTZ NOT NULL
	)`,
	`CREATE TABLE document_practice_revisions (
		id TEXT PRIMARY KEY,
		workflow_id TEXT NOT NULL REFERENCES document_workflows(id) ON DELETE RESTRICT,
		source_id TEXT NOT NULL,
		commit TEXT NOT NULL,
		language TEXT NOT NULL,
		page_path TEXT NOT NULL,
		anchor TEXT NOT NULL DEFAULT '',
		candidate_id TEXT NOT NULL,
		runnable_revision_id TEXT NOT NULL REFERENCES runnable_revisions(id) ON DELETE RESTRICT,
		runnable_revision_digest TEXT NOT NULL REFERENCES runnable_revisions(runnable_revision_digest) ON DELETE RESTRICT,
		verification_report_id TEXT NOT NULL REFERENCES runnable_verification_reports(id) ON DELETE RESTRICT,
		verification_report_digest TEXT NOT NULL REFERENCES runnable_verification_reports(verification_report_digest) ON DELETE RESTRICT,
		manifest_id TEXT NOT NULL REFERENCES document_publication_manifests(id) ON DELETE RESTRICT,
		revision JSONB NOT NULL,
		published_at TIMESTAMPTZ NOT NULL,
		UNIQUE(workflow_id),
		UNIQUE(source_id, commit, language, page_path, anchor)
	)`,
	`CREATE TABLE document_practice_index (
		source_id TEXT NOT NULL,
		commit TEXT NOT NULL,
		language TEXT NOT NULL,
		page_path TEXT NOT NULL,
		anchor TEXT NOT NULL DEFAULT '',
		practice_revision_id TEXT NOT NULL REFERENCES document_practice_revisions(id) ON DELETE RESTRICT,
		PRIMARY KEY (source_id, commit, language, page_path, anchor)
	)`,
	`CREATE TABLE document_batches (
		id TEXT PRIMARY KEY,
		state TEXT NOT NULL CHECK (state IN ('Pending','Running','Paused','Completed','Cancelled')),
		scope JSONB NOT NULL,
		concurrency INTEGER NOT NULL CHECK (concurrency BETWEEN 1 AND 8),
		resolution JSONB NOT NULL,
		total_items INTEGER NOT NULL CHECK (total_items >= 0),
		created_by TEXT NOT NULL,
		created_at TIMESTAMPTZ NOT NULL,
		updated_at TIMESTAMPTZ NOT NULL
	)`,
	`CREATE TABLE document_batch_items (
		id TEXT PRIMARY KEY,
		batch_id TEXT NOT NULL REFERENCES document_batches(id) ON DELETE RESTRICT,
		ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
		page_path TEXT NOT NULL,
		anchor TEXT NOT NULL DEFAULT '',
		title TEXT NOT NULL DEFAULT '',
		workflow_id TEXT NOT NULL DEFAULT '',
		state TEXT NOT NULL CHECK (state IN ('Pending','Scheduled','Running','Published','NoPractice','Rejected','Failed','Skipped','Cancelled')),
		detail TEXT NOT NULL DEFAULT '',
		created_at TIMESTAMPTZ NOT NULL,
		updated_at TIMESTAMPTZ NOT NULL,
		UNIQUE (batch_id, page_path, anchor),
		UNIQUE (batch_id, ordinal)
	)`,
	`CREATE INDEX document_batch_items_dispatch ON document_batch_items(batch_id, state, ordinal)`,
	`CREATE INDEX document_batch_items_workflow ON document_batch_items(workflow_id)`,
}
