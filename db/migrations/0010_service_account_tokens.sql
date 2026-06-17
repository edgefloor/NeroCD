ALTER TABLE api_tokens ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'service_account';
ALTER TABLE api_tokens ADD COLUMN IF NOT EXISTS expires_at TIMESTAMPTZ;

UPDATE api_tokens SET kind = 'service_account' WHERE kind = '';

ALTER TABLE api_tokens DROP CONSTRAINT IF EXISTS api_tokens_kind_check;
ALTER TABLE api_tokens ADD CONSTRAINT api_tokens_kind_check CHECK (kind IN ('service_account', 'bootstrap'));

ALTER TABLE users DROP CONSTRAINT IF EXISTS users_global_role_check;
ALTER TABLE users ADD CONSTRAINT users_global_role_check CHECK (global_role IN ('user', 'system_admin', 'runner_admin'));
