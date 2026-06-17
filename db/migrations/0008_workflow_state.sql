ALTER TABLE task_runs ADD COLUMN IF NOT EXISTS workflow_state JSONB NOT NULL DEFAULT '{"steps":[]}'::jsonb;
