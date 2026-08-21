-- AC16b: a single local backup scheduler.  It is intentionally disabled by
-- default; an upgrade never starts producing or rotating archives on its own.
CREATE TABLE IF NOT EXISTS backup_schedule (
  singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
  enabled BOOLEAN NOT NULL DEFAULT FALSE,
  interval_seconds INTEGER NOT NULL DEFAULT 86400 CHECK (interval_seconds BETWEEN 60 AND 604800),
  retention_count INTEGER NOT NULL DEFAULT 7 CHECK (retention_count BETWEEN 1 AND 365),
  next_run_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
  consecutive_failures INTEGER NOT NULL DEFAULT 0 CHECK (consecutive_failures BETWEEN 0 AND 8),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
INSERT INTO backup_schedule(singleton) VALUES (TRUE) ON CONFLICT (singleton) DO NOTHING;

CREATE TABLE IF NOT EXISTS backup_schedule_runs (
  id TEXT PRIMARY KEY,
  scheduled_for TIMESTAMPTZ NOT NULL,
  started_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
  completed_at TIMESTAMPTZ,
  status TEXT NOT NULL CHECK (status IN ('running','succeeded','failed')),
  reason TEXT NOT NULL CHECK (reason IN ('none','preflight','dump','publish','rotation','database'))
);
CREATE INDEX IF NOT EXISTS idx_backup_schedule_runs_started_at ON backup_schedule_runs(started_at DESC);

CREATE OR REPLACE FUNCTION nerocd_observability_schema_compatible() RETURNS boolean
LANGUAGE sql STABLE AS $$
SELECT to_regclass(current_schema() || '.runner_operational_observations') IS NOT NULL
   AND to_regclass(current_schema() || '.backup_operational_results') IS NOT NULL
   AND to_regclass(current_schema() || '.backup_schedule') IS NOT NULL
   AND to_regclass(current_schema() || '.backup_schedule_runs') IS NOT NULL
$$;
