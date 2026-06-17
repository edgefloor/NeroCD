ALTER TABLE runners ADD COLUMN IF NOT EXISTS token_hash TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_runners_token_hash ON runners(token_hash);
