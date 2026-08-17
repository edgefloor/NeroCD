ALTER TABLE run_logs
  ADD COLUMN event_key TEXT,
  ADD COLUMN lease_id TEXT,
  ADD COLUMN attempt INTEGER,
  ADD COLUMN requested_sequence INTEGER;

ALTER TABLE run_logs
  ADD CONSTRAINT run_logs_event_key_nonempty CHECK (event_key IS NULL OR event_key <> ''),
  ADD CONSTRAINT run_logs_attempt_positive CHECK (attempt IS NULL OR attempt > 0),
  ADD CONSTRAINT run_logs_requested_sequence_positive CHECK (requested_sequence IS NULL OR requested_sequence > 0),
  ADD CONSTRAINT run_logs_runner_event_shape CHECK (
    (event_key IS NULL AND lease_id IS NULL AND attempt IS NULL AND requested_sequence IS NULL)
    OR
    (event_key IS NOT NULL AND lease_id IS NOT NULL AND attempt IS NOT NULL AND requested_sequence IS NOT NULL)
  );

CREATE UNIQUE INDEX run_logs_run_id_event_key_unique
  ON run_logs (run_id, event_key)
  WHERE event_key IS NOT NULL;

ALTER TABLE run_leases ADD COLUMN completion_key TEXT;
ALTER TABLE run_leases ADD CONSTRAINT run_leases_completion_key_nonempty
  CHECK (completion_key IS NULL OR completion_key <> '');

CREATE OR REPLACE FUNCTION nerocd_replay_schema_compatible()
RETURNS boolean
LANGUAGE sql
STABLE
AS $function$
SELECT
  EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='run_logs' AND column_name='event_key' AND is_nullable='YES')
  AND EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='run_logs' AND column_name='lease_id' AND is_nullable='YES')
  AND EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='run_logs' AND column_name='attempt' AND is_nullable='YES')
  AND EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='run_logs' AND column_name='requested_sequence' AND is_nullable='YES')
  AND EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='run_leases' AND column_name='completion_key' AND is_nullable='YES')
  AND EXISTS (
    SELECT 1 FROM pg_index index_row
    JOIN pg_class index_relation ON index_relation.oid=index_row.indexrelid
    JOIN pg_class table_relation ON table_relation.oid=index_row.indrelid
    JOIN pg_namespace namespace_row ON namespace_row.oid=table_relation.relnamespace
    JOIN pg_attribute run_id ON run_id.attrelid=table_relation.oid AND run_id.attname='run_id'
    JOIN pg_attribute event_key ON event_key.attrelid=table_relation.oid AND event_key.attname='event_key'
    WHERE namespace_row.nspname=current_schema()
      AND table_relation.relname='run_logs'
      AND index_relation.relname='run_logs_run_id_event_key_unique'
      AND index_row.indisunique AND index_row.indisvalid AND index_row.indisready
      AND index_row.indexprs IS NULL
      AND index_row.indnkeyatts=2 AND index_row.indnatts=2
      AND index_row.indkey[0]=run_id.attnum AND index_row.indkey[1]=event_key.attnum
      AND regexp_replace(pg_get_expr(index_row.indpred, index_row.indrelid, true), '[[:space:]]', '', 'g')='event_keyISNOTNULL'
  );
$function$;
