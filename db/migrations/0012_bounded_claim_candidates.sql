CREATE INDEX IF NOT EXISTS idx_task_runs_queued_started_id
    ON task_runs (started_at, id)
    WHERE status = 'queued';
