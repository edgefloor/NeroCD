ALTER TABLE sessions ADD COLUMN IF NOT EXISTS source_ip TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS user_agent TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS last_seen_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS sessions_user_created_idx ON sessions (user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS sessions_active_idx ON sessions (revoked_at, expires_at);
