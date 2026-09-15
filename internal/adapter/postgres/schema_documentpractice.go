package postgres

// Document practice records are append-only. No update path is exposed for
// these tables; a revised plan or candidate receives a new artifact digest.
var schemaDocumentPracticeStatements = []string{
	`CREATE TABLE document_snapshots (
		source_id TEXT NOT NULL,
		commit TEXT NOT NULL,
		version TEXT NOT NULL,
		language TEXT NOT NULL,
		license TEXT NOT NULL,
		mirror_origin TEXT NOT NULL,
		mirror_digest TEXT NOT NULL,
		context JSONB NOT NULL,
		created_at TIMESTAMPTZ NOT NULL,
		PRIMARY KEY (source_id, commit, language, mirror_digest)
	)`,
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
		created_at TIMESTAMPTZ NOT NULL,
		UNIQUE(workflow_id, kind, digest)
	)`,
	`CREATE INDEX document_artifact_ledger_workflow ON document_artifact_ledger(workflow_id, created_at, id)`,
	`CREATE TABLE document_workflows (
		id TEXT PRIMARY KEY,
		state TEXT NOT NULL CHECK (state IN ('Planning','PlanReviewing','Generating','ArtifactReviewing','MaterializingArtifact','Verifying','VerificationReviewing','Publishing','Published','Rejected','Failed')),
		state_version BIGINT NOT NULL CHECK (state_version >= 1),
		revision BIGINT NOT NULL CHECK (revision >= 1),
		max_revisions BIGINT NOT NULL CHECK (max_revisions >= 1),
		lease_owner TEXT NOT NULL DEFAULT '',
		lease_expires_at TIMESTAMPTZ,
		updated_at TIMESTAMPTZ NOT NULL,
		CHECK ((lease_owner = '') = (lease_expires_at IS NULL))
	)`,
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
}
