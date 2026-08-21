CREATE TABLE IF NOT EXISTS users (
    id TEXT PRIMARY KEY,
    email TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('active', 'disabled', 'pending')),
    global_role TEXT NOT NULL DEFAULT 'user' CHECK (global_role IN ('user', 'system_admin', 'runner_admin')),
    password_hash TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    archived_at TIMESTAMPTZ
);

ALTER TABLE users ADD COLUMN IF NOT EXISTS password_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN IF NOT EXISTS global_role TEXT NOT NULL DEFAULT 'user';

CREATE TABLE IF NOT EXISTS sessions (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL UNIQUE,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    source_ip TEXT NOT NULL DEFAULT '',
    user_agent TEXT NOT NULL DEFAULT '',
    last_seen_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS api_tokens (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    kind TEXT NOT NULL DEFAULT 'service_account' CHECK (kind IN ('service_account', 'bootstrap')),
    token_hash TEXT NOT NULL UNIQUE,
    roles TEXT[] NOT NULL DEFAULT '{}',
    status TEXT NOT NULL CHECK (status IN ('active', 'revoked')),
    created_by TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ,
    last_used_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS projects (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE projects ADD COLUMN IF NOT EXISTS archived_at TIMESTAMPTZ;

CREATE TABLE IF NOT EXISTS repositories (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    url TEXT NOT NULL,
    provider TEXT NOT NULL DEFAULT '',
    default_ref TEXT NOT NULL DEFAULT 'main',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS task_templates (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    kind TEXT NOT NULL,
    run_spec JSONB NOT NULL DEFAULT '{}'::jsonb,
    workflow JSONB NOT NULL DEFAULT '{"steps":[]}'::jsonb,
    workflow_state JSONB NOT NULL DEFAULT '{"steps":[]}'::jsonb,
    runner_tags TEXT[] NOT NULL DEFAULT '{}',
    requires_ack BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE task_templates DROP CONSTRAINT IF EXISTS task_templates_kind_check;
ALTER TABLE task_templates ADD COLUMN IF NOT EXISTS run_spec JSONB NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE task_templates ADD COLUMN IF NOT EXISTS workflow JSONB NOT NULL DEFAULT '{"steps":[]}'::jsonb;

CREATE TABLE IF NOT EXISTS task_runs (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    template_id TEXT REFERENCES task_templates(id) ON DELETE SET NULL,
    run_spec JSONB NOT NULL DEFAULT '{}'::jsonb,
    workflow JSONB NOT NULL DEFAULT '{"steps":[]}'::jsonb,
    runner_tags TEXT[] NOT NULL DEFAULT '{}',
    status TEXT NOT NULL CHECK (status IN ('queued', 'waiting_approval', 'running', 'succeeded', 'failed', 'canceled')),
    runner_id TEXT,
    requested_by TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE task_runs ALTER COLUMN template_id DROP NOT NULL;
ALTER TABLE task_runs ADD COLUMN IF NOT EXISTS run_spec JSONB NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE task_runs ADD COLUMN IF NOT EXISTS workflow JSONB NOT NULL DEFAULT '{"steps":[]}'::jsonb;
ALTER TABLE task_runs ADD COLUMN IF NOT EXISTS workflow_state JSONB NOT NULL DEFAULT '{"steps":[]}'::jsonb;
ALTER TABLE task_runs ADD COLUMN IF NOT EXISTS runner_tags TEXT[] NOT NULL DEFAULT '{}';
ALTER TABLE task_runs ADD COLUMN IF NOT EXISTS runner_id TEXT;

CREATE TABLE IF NOT EXISTS runners (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    tags TEXT[] NOT NULL DEFAULT '{}',
    capabilities TEXT[] NOT NULL DEFAULT '{}',
    status TEXT NOT NULL CHECK (status IN ('active', 'stale', 'disabled', 'revoked')),
    registered_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_heartbeat_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS run_leases (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL REFERENCES task_runs(id) ON DELETE CASCADE,
    runner_id TEXT NOT NULL REFERENCES runners(id) ON DELETE CASCADE,
    status TEXT NOT NULL CHECK (status IN ('active', 'succeeded', 'failed', 'canceled', 'expired')),
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS run_logs (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL REFERENCES task_runs(id) ON DELETE CASCADE,
    sequence INTEGER NOT NULL,
    stream TEXT NOT NULL CHECK (stream IN ('system', 'stdout', 'stderr')),
    message TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (run_id, sequence)
);

CREATE TABLE IF NOT EXISTS run_artifacts (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL REFERENCES task_runs(id) ON DELETE CASCADE,
    lease_id TEXT NOT NULL REFERENCES run_leases(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    path TEXT NOT NULL,
    found BOOLEAN NOT NULL DEFAULT false,
    required BOOLEAN NOT NULL DEFAULT false,
    size BIGINT NOT NULL DEFAULT 0,
    kind TEXT NOT NULL DEFAULT 'file',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS approvals (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL UNIQUE REFERENCES task_runs(id) ON DELETE CASCADE,
    status TEXT NOT NULL CHECK (status IN ('pending', 'approved', 'rejected')),
    requested_by TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    approved_by TEXT REFERENCES users(id) ON DELETE RESTRICT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    approved_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS audit_events (
    id TEXT PRIMARY KEY,
    actor_id TEXT NOT NULL,
    action TEXT NOT NULL,
    target_id TEXT NOT NULL,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_sessions_user_id ON sessions(user_id);
CREATE INDEX IF NOT EXISTS idx_sessions_expires_at ON sessions(expires_at);
CREATE INDEX IF NOT EXISTS idx_api_tokens_token_hash ON api_tokens(token_hash);
CREATE INDEX IF NOT EXISTS idx_api_tokens_status ON api_tokens(status);
CREATE INDEX IF NOT EXISTS idx_task_templates_project_id ON task_templates(project_id);
CREATE INDEX IF NOT EXISTS idx_repositories_project_id ON repositories(project_id);
CREATE INDEX IF NOT EXISTS idx_task_runs_project_id ON task_runs(project_id);
CREATE INDEX IF NOT EXISTS idx_task_runs_template_id ON task_runs(template_id);
CREATE INDEX IF NOT EXISTS idx_task_runs_status ON task_runs(status);
CREATE INDEX IF NOT EXISTS idx_task_runs_runner_id ON task_runs(runner_id);
CREATE INDEX IF NOT EXISTS idx_runners_status ON runners(status);
CREATE INDEX IF NOT EXISTS idx_runners_last_heartbeat_at ON runners(last_heartbeat_at);
CREATE INDEX IF NOT EXISTS idx_run_leases_run_id ON run_leases(run_id);
CREATE INDEX IF NOT EXISTS idx_run_leases_runner_id ON run_leases(runner_id);
CREATE INDEX IF NOT EXISTS idx_run_leases_status ON run_leases(status);
CREATE INDEX IF NOT EXISTS idx_run_logs_run_id_sequence ON run_logs(run_id, sequence);
CREATE INDEX IF NOT EXISTS idx_run_artifacts_run_id ON run_artifacts(run_id);
CREATE INDEX IF NOT EXISTS idx_approvals_status ON approvals(status);
CREATE INDEX IF NOT EXISTS idx_audit_events_created_at ON audit_events(created_at);
CREATE INDEX IF NOT EXISTS idx_audit_events_target_id ON audit_events(target_id);
