-- Human users and managed service accounts share stable actor IDs. Registry
-- rows are append-only so historical actor foreign keys cannot be retargeted.
CREATE TABLE principals (
    id TEXT PRIMARY KEY,
    kind TEXT NOT NULL CHECK (kind IN ('human', 'service_account')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);

INSERT INTO principals (id, kind, created_at)
SELECT id, 'human', created_at FROM users;

CREATE OR REPLACE FUNCTION nerocd_register_principal()
RETURNS trigger LANGUAGE plpgsql AS $function$
DECLARE expected_kind TEXT := TG_ARGV[0]; existing_kind TEXT;
BEGIN
    SELECT kind INTO existing_kind FROM principals WHERE id = NEW.id;
    IF FOUND AND existing_kind <> expected_kind THEN
        RAISE EXCEPTION 'principal id already registered with another kind';
    END IF;
    INSERT INTO principals (id, kind, created_at)
    VALUES (NEW.id, expected_kind, COALESCE(NEW.created_at, clock_timestamp()))
    ON CONFLICT (id) DO NOTHING;
    RETURN NEW;
END;
$function$;

CREATE TRIGGER users_register_principal
BEFORE INSERT ON users FOR EACH ROW EXECUTE FUNCTION nerocd_register_principal('human');

ALTER TABLE users ADD CONSTRAINT users_principal_fk
    FOREIGN KEY (id) REFERENCES principals(id) ON DELETE RESTRICT;

CREATE TABLE service_accounts (
    id TEXT PRIMARY KEY REFERENCES principals(id) ON DELETE RESTRICT,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    owner_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    name TEXT NOT NULL CHECK (name <> ''),
    role TEXT NOT NULL CHECK (role IN ('owner', 'maintainer', 'viewer')),
    status TEXT NOT NULL CHECK (status IN ('active', 'revoked')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    revoked_at TIMESTAMPTZ,
    CHECK ((status = 'active' AND revoked_at IS NULL) OR (status = 'revoked' AND revoked_at IS NOT NULL))
);

CREATE TRIGGER service_accounts_register_principal
BEFORE INSERT ON service_accounts FOR EACH ROW EXECUTE FUNCTION nerocd_register_principal('service_account');

CREATE OR REPLACE FUNCTION nerocd_service_account_immutable_scope()
RETURNS trigger LANGUAGE plpgsql AS $function$
BEGIN
    IF NEW.id <> OLD.id OR NEW.project_id <> OLD.project_id OR NEW.owner_user_id <> OLD.owner_user_id THEN
        RAISE EXCEPTION 'service account identity, project, and owner are immutable';
    END IF;
    RETURN NEW;
END;
$function$;

CREATE TRIGGER service_accounts_immutable_scope
BEFORE UPDATE ON service_accounts FOR EACH ROW EXECUTE FUNCTION nerocd_service_account_immutable_scope();

CREATE OR REPLACE FUNCTION nerocd_principal_immutable()
RETURNS trigger LANGUAGE plpgsql AS $function$
BEGIN
    RAISE EXCEPTION 'principal registry is immutable';
END;
$function$;

CREATE TRIGGER principals_immutable
BEFORE UPDATE OR DELETE ON principals FOR EACH ROW EXECUTE FUNCTION nerocd_principal_immutable();

ALTER TABLE api_tokens ADD COLUMN service_account_id TEXT REFERENCES service_accounts(id) ON DELETE RESTRICT;
ALTER TABLE api_tokens ADD CONSTRAINT api_tokens_managed_scope_check CHECK (
    service_account_id IS NULL OR
    (kind = 'service_account' AND cardinality(roles) = 0 AND expires_at IS NOT NULL)
);

CREATE INDEX service_accounts_project_page_idx ON service_accounts (project_id, created_at DESC, id DESC);
CREATE INDEX service_accounts_active_owner_idx ON service_accounts (owner_user_id) WHERE status = 'active';
CREATE INDEX api_tokens_active_service_account_idx ON api_tokens (service_account_id) WHERE service_account_id IS NOT NULL AND status = 'active' AND revoked_at IS NULL;

-- These columns record an actor and can therefore refer to either human or
-- managed automation identities. Membership, login, and credential ownership
-- foreign keys remain users-only.
ALTER TABLE task_runs DROP CONSTRAINT task_runs_requested_by_fkey;
ALTER TABLE task_runs ADD CONSTRAINT task_runs_requested_by_fkey FOREIGN KEY (requested_by) REFERENCES principals(id) ON DELETE RESTRICT;
ALTER TABLE approvals DROP CONSTRAINT approvals_requested_by_fkey;
ALTER TABLE approvals ADD CONSTRAINT approvals_requested_by_fkey FOREIGN KEY (requested_by) REFERENCES principals(id) ON DELETE RESTRICT;
ALTER TABLE approvals DROP CONSTRAINT approvals_approved_by_fkey;
ALTER TABLE approvals ADD CONSTRAINT approvals_approved_by_fkey FOREIGN KEY (approved_by) REFERENCES principals(id) ON DELETE RESTRICT;
ALTER TABLE services DROP CONSTRAINT services_owner_id_fkey;
ALTER TABLE services ADD CONSTRAINT services_owner_id_fkey FOREIGN KEY (owner_id) REFERENCES principals(id) ON DELETE RESTRICT;
ALTER TABLE revisions DROP CONSTRAINT revisions_created_by_fkey;
ALTER TABLE revisions ADD CONSTRAINT revisions_created_by_fkey FOREIGN KEY (created_by) REFERENCES principals(id) ON DELETE RESTRICT;
ALTER TABLE deployments DROP CONSTRAINT deployments_requested_by_fkey;
ALTER TABLE deployments ADD CONSTRAINT deployments_requested_by_fkey FOREIGN KEY (requested_by) REFERENCES principals(id) ON DELETE RESTRICT;
ALTER TABLE deployments DROP CONSTRAINT deployments_confirmed_by_fkey;
ALTER TABLE deployments ADD CONSTRAINT deployments_confirmed_by_fkey FOREIGN KEY (confirmed_by) REFERENCES principals(id) ON DELETE RESTRICT;
ALTER TABLE repository_policy_configuration_receipts DROP CONSTRAINT repository_policy_receipts_actor_fk;
ALTER TABLE repository_policy_configuration_receipts ADD CONSTRAINT repository_policy_receipts_actor_fk FOREIGN KEY (actor_id) REFERENCES principals(id) ON DELETE RESTRICT;
ALTER TABLE deployment_cancellations DROP CONSTRAINT deployment_cancellations_actor_id_fkey;
ALTER TABLE deployment_cancellations ADD CONSTRAINT deployment_cancellations_actor_id_fkey FOREIGN KEY (actor_id) REFERENCES principals(id) ON DELETE RESTRICT;

CREATE OR REPLACE FUNCTION nerocd_service_account_schema_compatible()
RETURNS boolean LANGUAGE sql STABLE AS $function$
SELECT to_regclass(current_schema() || '.principals') IS NOT NULL
   AND to_regclass(current_schema() || '.service_accounts') IS NOT NULL
   AND to_regclass(current_schema() || '.service_accounts_project_page_idx') IS NOT NULL
   AND to_regclass(current_schema() || '.api_tokens_active_service_account_idx') IS NOT NULL
   AND EXISTS (SELECT 1 FROM pg_trigger WHERE tgrelid = 'users'::regclass AND tgname = 'users_register_principal' AND tgenabled <> 'D')
   AND EXISTS (SELECT 1 FROM pg_trigger WHERE tgrelid = 'service_accounts'::regclass AND tgname = 'service_accounts_register_principal' AND tgenabled <> 'D')
   AND EXISTS (SELECT 1 FROM pg_trigger WHERE tgrelid = 'service_accounts'::regclass AND tgname = 'service_accounts_immutable_scope' AND tgenabled <> 'D')
   AND EXISTS (SELECT 1 FROM pg_trigger WHERE tgrelid = 'principals'::regclass AND tgname = 'principals_immutable' AND tgenabled <> 'D')
   AND EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'users'::regclass AND conname = 'users_principal_fk' AND contype = 'f' AND confrelid = 'principals'::regclass AND convalidated)
   AND EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'api_tokens'::regclass AND conname = 'api_tokens_managed_scope_check' AND contype = 'c' AND convalidated)
   AND NOT EXISTS (
       SELECT 1 FROM (VALUES
           ('task_runs'::regclass, 'task_runs_requested_by_fkey'),
           ('approvals'::regclass, 'approvals_requested_by_fkey'),
           ('approvals'::regclass, 'approvals_approved_by_fkey'),
           ('services'::regclass, 'services_owner_id_fkey'),
           ('revisions'::regclass, 'revisions_created_by_fkey'),
           ('deployments'::regclass, 'deployments_requested_by_fkey'),
           ('deployments'::regclass, 'deployments_confirmed_by_fkey'),
           ('repository_policy_configuration_receipts'::regclass, 'repository_policy_receipts_actor_fk'),
           ('deployment_cancellations'::regclass, 'deployment_cancellations_actor_id_fkey')
       ) expected(table_oid, constraint_name)
       WHERE NOT EXISTS (
           SELECT 1 FROM pg_constraint c
           WHERE c.conrelid = expected.table_oid AND c.conname = expected.constraint_name
             AND c.contype = 'f' AND c.confrelid = 'principals'::regclass AND c.convalidated
       )
   )
   AND EXISTS (
       SELECT 1 FROM information_schema.columns
       WHERE table_schema = current_schema() AND table_name = 'api_tokens'
         AND column_name = 'service_account_id' AND is_nullable = 'YES'
   );
$function$;

-- Migration 0026 pins its actor FK target as part of readiness; update that
-- assertion after migrating the same named constraint to the shared registry.
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
 AND EXISTS (SELECT 1 FROM constraints WHERE conname='repository_policy_receipts_actor_fk' AND contype='f' AND confrelid='principals'::regclass AND confdeltype='r')
 AND EXISTS (SELECT 1 FROM constraints WHERE conname='repository_policy_receipts_audit_fk' AND contype='f' AND confrelid='audit_events'::regclass)
 AND EXISTS (SELECT 1 FROM constraints WHERE conname='repository_policy_receipts_configuration_id_format' AND contype='c' AND definition LIKE '%configuration_id%cfg_[A-Za-z0-9_-]{8,128}%')
 AND EXISTS (SELECT 1 FROM constraints WHERE conname='repository_policy_receipts_sha256_format' AND contype='c' AND definition LIKE '%policy_sha256%[0-9a-f]{64}%');
$function$;
