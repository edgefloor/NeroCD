ALTER TABLE task_runs ADD COLUMN IF NOT EXISTS next_attempt INTEGER;

UPDATE task_runs AS runs
SET next_attempt = COALESCE((
    SELECT MAX(leases.attempt) + 1
    FROM run_leases AS leases
    WHERE leases.run_id = runs.id
), 1)
WHERE next_attempt IS NULL;

ALTER TABLE task_runs ALTER COLUMN next_attempt SET DEFAULT 1;
ALTER TABLE task_runs ALTER COLUMN next_attempt SET NOT NULL;
ALTER TABLE task_runs ADD CONSTRAINT task_runs_next_attempt_positive CHECK (next_attempt > 0);

ALTER TABLE task_runs ADD COLUMN IF NOT EXISTS claim_order_at TIMESTAMPTZ;
UPDATE task_runs SET claim_order_at = started_at WHERE claim_order_at IS NULL;
ALTER TABLE task_runs ALTER COLUMN claim_order_at SET DEFAULT clock_timestamp();
ALTER TABLE task_runs ALTER COLUMN claim_order_at SET NOT NULL;

DROP INDEX IF EXISTS idx_task_runs_queued_started_id;
CREATE INDEX IF NOT EXISTS idx_task_runs_queued_claim_order_id
    ON task_runs (claim_order_at, id)
    WHERE status = 'queued';

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = current_schema()
          AND table_name = 'runner_claim_cursors'
          AND column_name = 'started_at'
    ) THEN
        ALTER TABLE runner_claim_cursors RENAME COLUMN started_at TO claim_order_at;
    END IF;
END $$;
