ALTER TABLE run_leases ADD COLUMN IF NOT EXISTS attempt INTEGER;
ALTER TABLE run_leases ADD COLUMN IF NOT EXISTS fence TEXT;
WITH ranked AS (
    SELECT id, row_number() OVER (PARTITION BY run_id ORDER BY created_at, id)::integer AS attempt
    FROM run_leases
)
UPDATE run_leases AS leases
SET attempt = ranked.attempt
FROM ranked
WHERE leases.id = ranked.id;
UPDATE run_leases
SET fence = replace(gen_random_uuid()::text, '-', '')
WHERE fence IS NULL OR fence = '';
ALTER TABLE run_leases ALTER COLUMN attempt SET NOT NULL;
ALTER TABLE run_leases ALTER COLUMN fence SET NOT NULL;
ALTER TABLE run_leases ALTER COLUMN attempt DROP DEFAULT;
ALTER TABLE run_leases ALTER COLUMN fence DROP DEFAULT;
ALTER TABLE run_leases ADD CONSTRAINT run_leases_attempt_positive CHECK (attempt > 0);
ALTER TABLE run_leases ADD CONSTRAINT run_leases_fence_nonempty CHECK (length(fence) > 0);
CREATE UNIQUE INDEX IF NOT EXISTS run_leases_run_id_attempt_unique ON run_leases (run_id, attempt);
CREATE UNIQUE INDEX IF NOT EXISTS run_leases_one_active_per_run ON run_leases (run_id) WHERE status = 'active';
CREATE INDEX IF NOT EXISTS run_leases_active_expiry ON run_leases (expires_at) WHERE status = 'active';
