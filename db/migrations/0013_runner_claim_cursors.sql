CREATE TABLE IF NOT EXISTS runner_claim_cursors (
    runner_id TEXT PRIMARY KEY REFERENCES runners(id) ON DELETE CASCADE,
    started_at TIMESTAMPTZ,
    run_id TEXT,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT runner_claim_cursors_tuple_complete CHECK (
        (started_at IS NULL AND run_id IS NULL) OR
        (started_at IS NOT NULL AND run_id IS NOT NULL)
    )
);
