-- Durable SSH control is separate from runner enrollment and deployment authority.
CREATE TABLE server_bootstrap_operations (
 id text PRIMARY KEY,
 project_id text NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
 actor_id text NOT NULL,
 request_key text NOT NULL CHECK(length(request_key) BETWEEN 1 AND 128),
 request_hash text NOT NULL CHECK(request_hash ~ '^sha256:[0-9a-f]{64}$'),
 target text NOT NULL,
 body jsonb NOT NULL CHECK(jsonb_typeof(body)='object' AND octet_length(body::text)<=16384),
 version bigint NOT NULL CHECK(version>0),
 state text NOT NULL CHECK(state IN ('draft','reviewed','running','installed_waiting_enrollment','registered','completed','refused','canceled','uncertain')),
 remote_hold boolean NOT NULL DEFAULT false,
 restore_held boolean NOT NULL DEFAULT false,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 CHECK(COALESCE(body->'ids'->>'operation_id'=id AND body->'input'->>'project_id'=project_id AND body->>'created_by'=actor_id AND body->>'request_hash'=request_hash,false)),
 UNIQUE(actor_id,project_id,request_key)
);
CREATE UNIQUE INDEX server_bootstrap_target_owned ON server_bootstrap_operations(target) WHERE state NOT IN ('canceled','refused');
CREATE TABLE server_bootstrap_commands (
 id text PRIMARY KEY,
 operation_id text NOT NULL REFERENCES server_bootstrap_operations(id) ON DELETE RESTRICT,
 actor_id text NOT NULL,
 request_key text NOT NULL CHECK(length(request_key) BETWEEN 1 AND 128),
 request_hash text NOT NULL CHECK(request_hash ~ '^sha256:[0-9a-f]{64}$'),
 action text NOT NULL CHECK(action IN ('probe','review','install','inspect_owned','register_server','grant_destination','cancel')),
 body jsonb NOT NULL CHECK(jsonb_typeof(body)='object' AND octet_length(body::text)<=32768),
 deadline timestamptz NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 lease_id text NOT NULL DEFAULT '',
 lease_expires_at timestamptz,
 attempt integer NOT NULL DEFAULT 0 CHECK(attempt BETWEEN 0 AND 8),
 fence bigint NOT NULL DEFAULT 0 CHECK(fence>=0),
 effect_intent boolean NOT NULL DEFAULT false,
 receipt jsonb CHECK(receipt IS NULL OR jsonb_typeof(receipt)='object' AND octet_length(receipt::text)<=32768),
 CHECK(deadline>created_at AND deadline<=created_at+interval '1 hour'),
 CHECK(COALESCE(body->>'id'=id AND body->>'operation_id'=operation_id AND body->>'actor_id'=actor_id AND body->>'key'=request_key AND body->>'request_hash'=request_hash AND body->>'action'=action,false)),
 UNIQUE(operation_id,request_key)
);
CREATE UNIQUE INDEX server_bootstrap_one_pending ON server_bootstrap_commands(operation_id) WHERE receipt IS NULL AND action<>'inspect_owned';
CREATE TABLE server_bootstrap_secret_grants (
 operation_id text PRIMARY KEY REFERENCES server_bootstrap_operations(id) ON DELETE RESTRICT,
 principal_id text NOT NULL,
 purpose text NOT NULL CHECK(purpose='ssh_bootstrap'),
 target_hash text NOT NULL,
 secret_id text NOT NULL,
 secret_version bigint NOT NULL CHECK(secret_version>0),
 expires_at timestamptz NOT NULL,
 revoked_at timestamptz,
 FOREIGN KEY(secret_id,secret_version) REFERENCES project_managed_secret_versions(secret_id,version) ON DELETE RESTRICT
);
CREATE TABLE server_bootstrap_enrollment_credentials (
 operation_id text PRIMARY KEY REFERENCES server_bootstrap_operations(id) ON DELETE RESTRICT,
 enrollment_id text NOT NULL UNIQUE REFERENCES runner_enrollments(id) ON DELETE RESTRICT,
 envelope_version integer NOT NULL CHECK(envelope_version=1),
 key_id text NOT NULL,
 ciphertext bytea NOT NULL CHECK(octet_length(ciphertext) BETWEEN 29 AND 65564)
);
CREATE TRIGGER server_bootstrap_enrollment_immutable BEFORE UPDATE OR DELETE ON server_bootstrap_enrollment_credentials FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_execution_immutable();
CREATE FUNCTION nerocd_server_bootstrap_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'bootstrap history is immutable'; END IF;
 IF TG_TABLE_NAME='server_bootstrap_operations' THEN
  IF (NEW.id,NEW.project_id,NEW.actor_id,NEW.request_key,NEW.request_hash,NEW.target,NEW.body,NEW.created_at) IS DISTINCT FROM (OLD.id,OLD.project_id,OLD.actor_id,OLD.request_key,OLD.request_hash,OLD.target,OLD.body,OLD.created_at) OR NEW.version<OLD.version OR OLD.remote_hold AND NOT NEW.remote_hold OR OLD.restore_held AND NOT NEW.restore_held THEN RAISE EXCEPTION 'bootstrap identity is immutable'; END IF;
 ELSE
  IF (NEW.id,NEW.operation_id,NEW.actor_id,NEW.request_key,NEW.request_hash,NEW.action,NEW.body,NEW.deadline,NEW.created_at) IS DISTINCT FROM (OLD.id,OLD.operation_id,OLD.actor_id,OLD.request_key,OLD.request_hash,OLD.action,OLD.body,OLD.deadline,OLD.created_at) OR OLD.receipt IS NOT NULL AND NEW.receipt IS DISTINCT FROM OLD.receipt OR NEW.attempt<OLD.attempt OR NEW.fence<OLD.fence OR OLD.effect_intent AND NOT NEW.effect_intent THEN RAISE EXCEPTION 'bootstrap command is immutable'; END IF;
 END IF;
 RETURN NEW;
END;$$;
CREATE TRIGGER server_bootstrap_operation_identity BEFORE UPDATE OR DELETE ON server_bootstrap_operations FOR EACH ROW EXECUTE FUNCTION nerocd_server_bootstrap_immutable();
CREATE TRIGGER server_bootstrap_command_identity BEFORE UPDATE OR DELETE ON server_bootstrap_commands FOR EACH ROW EXECUTE FUNCTION nerocd_server_bootstrap_immutable();
CREATE FUNCTION nerocd_server_bootstrap_grant_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' OR (NEW.operation_id,NEW.principal_id,NEW.purpose,NEW.target_hash,NEW.secret_id,NEW.secret_version,NEW.expires_at) IS DISTINCT FROM (OLD.operation_id,OLD.principal_id,OLD.purpose,OLD.target_hash,OLD.secret_id,OLD.secret_version,OLD.expires_at) OR OLD.revoked_at IS NOT NULL AND NEW.revoked_at IS DISTINCT FROM OLD.revoked_at THEN RAISE EXCEPTION 'bootstrap grant identity is immutable'; END IF;
 RETURN NEW;
END;$$;
CREATE TRIGGER server_bootstrap_grant_identity BEFORE UPDATE OR DELETE ON server_bootstrap_secret_grants FOR EACH ROW EXECUTE FUNCTION nerocd_server_bootstrap_grant_immutable();
CREATE FUNCTION nerocd_server_bootstrap_credential_consistent() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE o server_bootstrap_operations%ROWTYPE; e runner_enrollments%ROWTYPE;
BEGIN
 SELECT * INTO STRICT o FROM server_bootstrap_operations WHERE id=NEW.operation_id;
 SELECT * INTO STRICT e FROM runner_enrollments WHERE id=NEW.enrollment_id;
 IF o.body->'ids'->>'enrollment_id' IS DISTINCT FROM e.id OR o.body->'ids'->>'runner_id' IS DISTINCT FROM e.runner_id OR o.actor_id<>e.created_by OR NOT o.remote_hold OR NOT EXISTS(SELECT 1 FROM server_bootstrap_commands WHERE operation_id=o.id AND action='install' AND effect_intent) THEN RAISE EXCEPTION 'bootstrap enrollment evidence differs'; END IF;
 RETURN NULL;
END;$$;
CREATE CONSTRAINT TRIGGER server_bootstrap_credential_consistent AFTER INSERT ON server_bootstrap_enrollment_credentials DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION nerocd_server_bootstrap_credential_consistent();
CREATE FUNCTION nerocd_server_bootstrap_schema_compatible() RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT to_regclass(current_schema()||'.server_bootstrap_operations') IS NOT NULL
 AND to_regclass(current_schema()||'.server_bootstrap_commands') IS NOT NULL
 AND to_regclass(current_schema()||'.server_bootstrap_secret_grants') IS NOT NULL
 AND to_regclass(current_schema()||'.server_bootstrap_enrollment_credentials') IS NOT NULL
 AND EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass(current_schema()||'.server_bootstrap_operations') AND tgname='server_bootstrap_operation_identity' AND tgenabled='O')
 AND EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass(current_schema()||'.server_bootstrap_commands') AND tgname='server_bootstrap_command_identity' AND tgenabled='O')
 AND EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass(current_schema()||'.server_bootstrap_enrollment_credentials') AND tgname='server_bootstrap_enrollment_immutable' AND tgenabled='O')
 AND EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass(current_schema()||'.server_bootstrap_secret_grants') AND tgname='server_bootstrap_grant_identity' AND tgenabled='O')
 AND EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass(current_schema()||'.server_bootstrap_enrollment_credentials') AND tgname='server_bootstrap_credential_consistent' AND tgdeferrable AND tginitdeferred AND tgenabled='O')
 AND (SELECT count(*) FROM pg_constraint WHERE conrelid=to_regclass(current_schema()||'.server_bootstrap_secret_grants') AND contype='f' AND confdeltype='r' AND convalidated)=2
 AND (SELECT count(*) FROM pg_constraint WHERE conrelid=to_regclass(current_schema()||'.server_bootstrap_enrollment_credentials') AND contype='f' AND confdeltype='r' AND convalidated)=2;
$$;
DO $$ BEGIN
 EXECUTE format('ALTER FUNCTION %I.nerocd_server_bootstrap_grant_immutable() SET search_path TO %I, pg_catalog, pg_temp',current_schema(),current_schema());
 EXECUTE format('ALTER FUNCTION %I.nerocd_server_bootstrap_credential_consistent() SET search_path TO %I, pg_catalog, pg_temp',current_schema(),current_schema());
 EXECUTE format('ALTER FUNCTION %I.nerocd_server_bootstrap_immutable() SET search_path TO %I, pg_catalog, pg_temp',current_schema(),current_schema());
 EXECUTE format('ALTER FUNCTION %I.nerocd_server_bootstrap_schema_compatible() SET search_path TO %I, pg_catalog, pg_temp',current_schema(),current_schema());
END;$$;
