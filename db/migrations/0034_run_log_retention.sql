-- Manual run-log retention is deliberately independent of artifact storage and
-- audit evidence.  The singleton starts disabled, so upgrades cannot delete
-- historical output.
CREATE TABLE IF NOT EXISTS run_log_retention_policy (
  singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
  enabled BOOLEAN NOT NULL DEFAULT FALSE,
  keep_days INTEGER NOT NULL DEFAULT 30 CHECK (keep_days BETWEEN 1 AND 3650),
  batch_size INTEGER NOT NULL DEFAULT 1000 CHECK (batch_size BETWEEN 1 AND 10000),
  version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
  updated_by TEXT NOT NULL DEFAULT '',
  updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
INSERT INTO run_log_retention_policy(singleton) VALUES (TRUE) ON CONFLICT (singleton) DO NOTHING;

CREATE TABLE IF NOT EXISTS run_log_retention_receipts (
  request_id TEXT PRIMARY KEY CHECK (request_id <> ''),
  body_sha256 TEXT NOT NULL CHECK (body_sha256 <> ''),
  keep_days INTEGER NOT NULL CHECK (keep_days BETWEEN 1 AND 3650),
  batch_size INTEGER NOT NULL CHECK (batch_size BETWEEN 1 AND 10000),
  policy_version INTEGER NOT NULL,
  cutoff TIMESTAMPTZ NOT NULL,
  eligible_logs BIGINT NOT NULL CHECK (eligible_logs >= 0),
  eligible_bytes BIGINT NOT NULL CHECK (eligible_bytes >= 0),
  deleted_count BIGINT NOT NULL CHECK (deleted_count >= 0),
  deleted_bytes BIGINT NOT NULL CHECK (deleted_bytes >= 0),
  audit_id TEXT NOT NULL UNIQUE REFERENCES audit_events(id) ON DELETE RESTRICT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX IF NOT EXISTS idx_run_logs_retention_candidates ON run_logs(created_at, run_id);

CREATE OR REPLACE FUNCTION nerocd_retention_schema_compatible()
RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema=current_schema() AND table_name='run_log_retention_policy')
    AND EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema=current_schema() AND table_name='run_log_retention_receipts');
$$;
