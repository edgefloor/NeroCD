ALTER TABLE users ADD COLUMN IF NOT EXISTS global_role TEXT NOT NULL DEFAULT 'user';

ALTER TABLE users DROP CONSTRAINT IF EXISTS users_global_role_check;
ALTER TABLE users ADD CONSTRAINT users_global_role_check CHECK (global_role IN ('user', 'system_admin', 'runner_admin'));

UPDATE users SET global_role = 'system_admin' WHERE id = 'usr_bootstrap';

ALTER TABLE runners DROP CONSTRAINT IF EXISTS runners_status_check;
ALTER TABLE runners ADD CONSTRAINT runners_status_check CHECK (status IN ('active', 'stale', 'disabled', 'revoked'));
