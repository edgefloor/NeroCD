-- The 0014 DO block performs this rename on PostgreSQL. These idempotent
-- statements also expose the final column shape to static schema analyzers.
ALTER TABLE runner_claim_cursors ADD COLUMN IF NOT EXISTS claim_order_at TIMESTAMPTZ;
ALTER TABLE runner_claim_cursors DROP COLUMN IF EXISTS started_at;
