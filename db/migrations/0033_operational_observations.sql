-- AC15: bounded runner/backup observations. Each runner retains only its
-- latest authenticated aggregate; backup history is intentionally small and
-- contains no path, archive, credential, or free-form error detail.
CREATE TABLE IF NOT EXISTS runner_operational_observations (
    runner_id TEXT PRIMARY KEY REFERENCES runners(id) ON DELETE CASCADE,
    observed_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    journal_depth INTEGER NOT NULL CHECK (journal_depth >= 0 AND journal_depth <= 8192),
    retry_count INTEGER NOT NULL CHECK (retry_count >= 0 AND retry_count <= 100000),
    renew_failures INTEGER NOT NULL CHECK (renew_failures >= 0 AND renew_failures <= 100000)
);

CREATE TABLE IF NOT EXISTS backup_operational_results (
    id BIGSERIAL PRIMARY KEY,
    completed_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    outcome TEXT NOT NULL CHECK (outcome IN ('success','failure')),
    reason TEXT NOT NULL CHECK (reason IN ('none','preflight','dump','publish','database'))
);
CREATE INDEX IF NOT EXISTS idx_backup_operational_results_completed_at ON backup_operational_results(completed_at DESC);

CREATE OR REPLACE FUNCTION nerocd_observability_schema_compatible() RETURNS boolean
LANGUAGE sql STABLE AS $$
SELECT to_regclass(current_schema() || '.runner_operational_observations') IS NOT NULL
   AND to_regclass(current_schema() || '.backup_operational_results') IS NOT NULL
$$;
