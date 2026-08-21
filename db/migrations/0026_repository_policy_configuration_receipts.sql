-- A configuration request is an immutable one-shot admission decision.  The
-- receipt intentionally contains only a hash of canonical policy JSON; it is
-- not another copy of network policy or an opaque credential reference.
CREATE TABLE repository_policy_configuration_receipts (
    repository_id TEXT NOT NULL CONSTRAINT repository_policy_receipts_repository_fk REFERENCES repositories(id) ON DELETE CASCADE,
    configuration_id TEXT NOT NULL CONSTRAINT repository_policy_receipts_configuration_id_format CHECK (configuration_id ~ '^cfg_[A-Za-z0-9_-]{8,128}$'),
    actor_id TEXT NOT NULL CONSTRAINT repository_policy_receipts_actor_fk REFERENCES users(id),
    policy_sha256 TEXT NOT NULL CONSTRAINT repository_policy_receipts_sha256_format CHECK (policy_sha256 ~ '^[0-9a-f]{64}$'),
    audit_id TEXT NOT NULL CONSTRAINT repository_policy_receipts_audit_unique UNIQUE CONSTRAINT repository_policy_receipts_audit_fk REFERENCES audit_events(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT repository_policy_receipts_pk PRIMARY KEY (repository_id, configuration_id)
);

CREATE OR REPLACE FUNCTION nerocd_repository_policy_schema_compatible() RETURNS boolean LANGUAGE sql STABLE AS $function$
WITH receipt AS (SELECT to_regclass('repository_policy_configuration_receipts') AS oid),
columns_ok AS (
 SELECT count(*) = 6
 FROM pg_attribute a, receipt r
 WHERE a.attrelid=r.oid AND a.attnum > 0 AND NOT a.attisdropped
   AND (a.attname,a.atttypid::regtype::text,a.attnotnull) IN
     (('repository_id','text',true),('configuration_id','text',true),('actor_id','text',true),('policy_sha256','text',true),('audit_id','text',true),('created_at','timestamp with time zone',true))
), constraints AS (
 SELECT c.*, pg_get_constraintdef(c.oid, true) AS definition
 FROM pg_constraint c, receipt r WHERE c.conrelid=r.oid
), indexes_ok AS (
 SELECT count(*) = 2
 FROM constraints c JOIN pg_index i ON i.indexrelid=c.conindid
 WHERE c.conname IN ('repository_policy_receipts_pk','repository_policy_receipts_audit_unique')
   AND i.indisvalid AND i.indisready AND i.indpred IS NULL AND i.indexprs IS NULL AND i.indnkeyatts=i.indnatts
)
SELECT
 EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='repositories' AND column_name='repository_policy' AND is_nullable='NO')
 AND (SELECT oid IS NOT NULL FROM receipt)
 AND (SELECT * FROM columns_ok)
 AND (SELECT * FROM indexes_ok)
 AND NOT EXISTS (SELECT 1 FROM constraints WHERE conname IN ('repository_policy_receipts_pk','repository_policy_receipts_audit_unique','repository_policy_receipts_repository_fk','repository_policy_receipts_actor_fk','repository_policy_receipts_audit_fk','repository_policy_receipts_configuration_id_format','repository_policy_receipts_sha256_format') AND (NOT convalidated OR condeferrable OR condeferred))
 AND EXISTS (SELECT 1 FROM constraints WHERE conname='repository_policy_receipts_pk' AND contype='p' AND conkey=ARRAY[(SELECT attnum FROM pg_attribute,receipt WHERE attrelid=receipt.oid AND attname='repository_id'),(SELECT attnum FROM pg_attribute,receipt WHERE attrelid=receipt.oid AND attname='configuration_id')])
 AND EXISTS (SELECT 1 FROM constraints WHERE conname='repository_policy_receipts_audit_unique' AND contype='u' AND conkey=ARRAY[(SELECT attnum FROM pg_attribute,receipt WHERE attrelid=receipt.oid AND attname='audit_id')])
 AND EXISTS (SELECT 1 FROM constraints WHERE conname='repository_policy_receipts_repository_fk' AND contype='f' AND confrelid='repositories'::regclass AND confdeltype='c')
 AND EXISTS (SELECT 1 FROM constraints WHERE conname='repository_policy_receipts_actor_fk' AND contype='f' AND confrelid='users'::regclass AND confdeltype='a')
 AND EXISTS (SELECT 1 FROM constraints WHERE conname='repository_policy_receipts_audit_fk' AND contype='f' AND confrelid='audit_events'::regclass AND confdeltype='a')
 AND EXISTS (SELECT 1 FROM constraints WHERE conname='repository_policy_receipts_configuration_id_format' AND contype='c' AND definition LIKE '%configuration_id%cfg_[A-Za-z0-9_-]{8,128}%')
 AND EXISTS (SELECT 1 FROM constraints WHERE conname='repository_policy_receipts_sha256_format' AND contype='c' AND definition LIKE '%policy_sha256%[0-9a-f]{64}%');
$function$;
