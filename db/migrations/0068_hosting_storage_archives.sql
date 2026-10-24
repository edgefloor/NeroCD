-- Destination-local archives use separate plans, task targets and receipts.
-- They never acquire an application apply plan or claim application readiness.
CREATE TABLE hosting_storage_archive_plans (
 operation_id text PRIMARY KEY REFERENCES hosting_operations(id) ON DELETE RESTRICT,
 mode text NOT NULL CHECK(mode IN ('backup','restore','recover')),
 backup_id text NOT NULL,
 source_resource_id text NOT NULL REFERENCES resources(id),
 source_binding_id text NOT NULL REFERENCES resource_bindings(id),
 source_storage_id text NOT NULL REFERENCES hosting_storage(id),
 target_resource_id text REFERENCES resources(id),
 target_binding_id text REFERENCES resource_bindings(id),
 target_storage_id text REFERENCES hosting_storage(id),
 recovery_of_operation_id text REFERENCES hosting_storage_archive_plans(operation_id),
 body jsonb NOT NULL CHECK(jsonb_typeof(body)='object' AND octet_length(body::text)<=32768),
 body_hash text NOT NULL CHECK(body_hash ~ '^sha256:[0-9a-f]{64}$'),
 UNIQUE(operation_id,body_hash),
 CHECK((target_resource_id IS NULL)=(target_binding_id IS NULL) AND (target_resource_id IS NULL)=(target_storage_id IS NULL)),
 CHECK(target_resource_id IS NULL OR (target_resource_id<>source_resource_id AND target_binding_id<>source_binding_id AND target_storage_id<>source_storage_id)),
 CHECK((mode='backup' AND target_resource_id IS NULL AND recovery_of_operation_id IS NULL) OR (mode='restore' AND target_resource_id IS NOT NULL AND recovery_of_operation_id IS NULL) OR (mode='recover' AND recovery_of_operation_id IS NOT NULL)),
 CHECK((body->>'operation_id'=operation_id AND body->>'mode'=mode AND body->>'backup_id'=backup_id AND body->>'hash'=body_hash AND body->>'compiler_version'='owned-upload.local-archive.v1' AND body->>'policy_version'='owned-upload.local-archive.v1' AND body->'source_storage'->>'id'=source_storage_id AND body->'source_storage'->>'resource_id'=source_resource_id AND body->'source_storage'->>'binding_id'=source_binding_id) IS TRUE),
 CHECK((jsonb_typeof(body->'limits')='object' AND (body->'limits'->>'archive_bytes')::bigint BETWEEN 1536 AND 1073741824 AND (body->'limits'->>'data_bytes')::bigint BETWEEN 1 AND (body->'limits'->>'archive_bytes')::bigint AND (body->'limits'->>'file_bytes')::bigint BETWEEN 1 AND (body->'limits'->>'data_bytes')::bigint AND (body->'limits'->>'members')::integer BETWEEN 1 AND 1000000 AND (body->'limits'->>'depth')::integer BETWEEN 1 AND 128 AND (body->'limits'->>'path_bytes')::integer BETWEEN 1 AND 255) IS TRUE),
 CHECK((body->'helper'->>'mode'='local_image' AND body->'helper'->>'image_id' ~ '^sha256:[0-9a-f]{64}$' AND body->'helper'->>'executable_sha256' ~ '^sha256:[0-9a-f]{64}$' AND body->'helper'->>'source_commit' ~ '^[0-9a-f]{40}$' AND body->'helper'->>'os'='linux' AND body->'helper'->>'architecture'='arm64' AND coalesce(body->'helper'->>'variant','')='') IS TRUE),
 CHECK((target_resource_id IS NULL AND jsonb_array_length(body->'writers') BETWEEN 1 AND 64) OR (target_resource_id IS NOT NULL AND body->'writers' IN ('[]'::jsonb,'null'::jsonb)))
);
CREATE TABLE hosting_storage_backups (
 id text PRIMARY KEY,
 operation_id text NOT NULL UNIQUE REFERENCES hosting_storage_archive_plans(operation_id),
 project_id text NOT NULL REFERENCES projects(id),
 resource_id text NOT NULL REFERENCES resources(id),
 request_id text NOT NULL CHECK(request_id ~ '^[0-9a-f]{32}$'),
 request_hash text NOT NULL CHECK(request_hash ~ '^sha256:[0-9a-f]{64}$'),
 created_by text NOT NULL REFERENCES principals(id),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(resource_id,request_id)
);
CREATE TABLE hosting_storage_restores (
 operation_id text PRIMARY KEY REFERENCES hosting_storage_archive_plans(operation_id),
 backup_id text NOT NULL REFERENCES hosting_storage_backups(id),
 resource_id text NOT NULL UNIQUE REFERENCES resources(id),
 request_id text NOT NULL CHECK(request_id ~ '^[0-9a-f]{32}$'),
 request_hash text NOT NULL CHECK(request_hash ~ '^sha256:[0-9a-f]{64}$'),
 created_by text NOT NULL REFERENCES principals(id),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(backup_id,request_id)
);
CREATE TABLE hosting_storage_archive_recoveries (
 operation_id text PRIMARY KEY REFERENCES hosting_storage_archive_plans(operation_id),
 original_operation_id text NOT NULL REFERENCES hosting_storage_archive_plans(operation_id),
 request_id text NOT NULL CHECK(request_id ~ '^[0-9a-f]{32}$'),
 request_hash text NOT NULL CHECK(request_hash ~ '^sha256:[0-9a-f]{64}$'),
 created_by text NOT NULL REFERENCES principals(id),
 UNIQUE(original_operation_id,request_id)
);
CREATE TABLE hosting_storage_archive_task_targets (
 run_id text PRIMARY KEY REFERENCES task_runs(id),
 operation_id text NOT NULL UNIQUE REFERENCES hosting_storage_archive_plans(operation_id),
 runner_id text NOT NULL REFERENCES runners(id),
 mode text NOT NULL CHECK(mode IN ('backup','restore','recover'))
);
CREATE TABLE hosting_storage_archive_receipts (
 operation_id text NOT NULL REFERENCES hosting_storage_archive_plans(operation_id),
 phase text NOT NULL CHECK(phase IN ('prepared','apply_begin','writer_snapshot','stop_intent','stopped','helper_intent','helper_identified','archive_published','restore_verified','resume_intent','resumed','complete')),
 receipt_id text NOT NULL UNIQUE CHECK(octet_length(receipt_id) BETWEEN 1 AND 128),
 authority jsonb NOT NULL CHECK(jsonb_typeof(authority)='object' AND octet_length(authority::text)<=2048),
 body jsonb NOT NULL CHECK(jsonb_typeof(body)='object' AND octet_length(body::text)<=32768),
 body_hash text NOT NULL CHECK(body_hash ~ '^sha256:[0-9a-f]{64}$'),
 plan_hash text NOT NULL,
 audit_id text NOT NULL UNIQUE REFERENCES audit_events(id) DEFERRABLE INITIALLY DEFERRED,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(operation_id,phase),
 FOREIGN KEY(operation_id,plan_hash) REFERENCES hosting_storage_archive_plans(operation_id,body_hash),
 CHECK((authority->>'operation_id'=operation_id AND body @> authority AND body->>'plan_hash'=plan_hash AND body->>'receipt_id'=receipt_id) IS TRUE)
);
CREATE TABLE hosting_storage_archive_blobs (
 backup_id text PRIMARY KEY REFERENCES hosting_storage_backups(id),
 operation_id text NOT NULL,
 phase text NOT NULL DEFAULT 'archive_published' CHECK(phase='archive_published'),
 body jsonb NOT NULL CHECK((jsonb_typeof(body)='object' AND octet_length(body::text)<=2048 AND body->>'backup_id'=backup_id AND body->>'sha256' ~ '^sha256:[0-9a-f]{64}$' AND (body->>'size_bytes')::bigint BETWEEN 1 AND 1073741824 AND body->>'format'='nerocd-storage-v1') IS TRUE),
 FOREIGN KEY(operation_id,phase) REFERENCES hosting_storage_archive_receipts(operation_id,phase) DEFERRABLE INITIALLY DEFERRED
);
-- A held restore target cannot enter the existing image-origin FK family. The
-- sibling preserves irreversible creation history without rewriting old evidence.
CREATE TABLE hosting_storage_restore_origins (
 storage_id text PRIMARY KEY REFERENCES hosting_storage(id),
 origin_operation_id text NOT NULL,
 origin_plan_hash text NOT NULL,
 origin jsonb NOT NULL CHECK((jsonb_typeof(origin)='object' AND octet_length(origin::text)<=16384 AND origin->>'state'='create_admitted' AND origin->'storage_ref'->>'storage_id'=storage_id AND origin->>'creation_operation_id'=origin_operation_id AND origin->>'creation_plan_hash'=origin_plan_hash) IS TRUE),
 origin_hash text NOT NULL CHECK(origin_hash ~ '^sha256:[0-9a-f]{64}$'),
 authority jsonb NOT NULL CHECK((jsonb_typeof(authority)='object' AND authority->>'operation_id'=origin_operation_id AND octet_length(authority::text)<=2048) IS TRUE),
 FOREIGN KEY(origin_operation_id,origin_plan_hash) REFERENCES hosting_storage_archive_plans(operation_id,body_hash),
 UNIQUE(storage_id,origin_operation_id)
);
CREATE TABLE hosting_storage_restore_materialized (
 storage_id text PRIMARY KEY,
 operation_id text NOT NULL,
 receipt_id text NOT NULL UNIQUE CHECK(octet_length(receipt_id) BETWEEN 1 AND 128),
 authority jsonb NOT NULL CHECK(jsonb_typeof(authority)='object' AND octet_length(authority::text)<=2048),
 body jsonb NOT NULL CHECK(jsonb_typeof(body)='object' AND octet_length(body::text)<=16384),
 body_hash text NOT NULL CHECK(body_hash ~ '^sha256:[0-9a-f]{64}$'),
 audit_id text NOT NULL UNIQUE REFERENCES audit_events(id) DEFERRABLE INITIALLY DEFERRED,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(storage_id,operation_id) REFERENCES hosting_storage_restore_origins(storage_id,origin_operation_id),
 CHECK((body @> authority AND body->>'receipt_id'=receipt_id AND body->'storage_ref'->>'storage_id'=storage_id AND body->>'driver'='local' AND body->>'scope'='local' AND body->'options' IN ('{}'::jsonb,'null'::jsonb)) IS TRUE)
);
CREATE TABLE hosting_storage_restore_use_admissions (
 operation_id text PRIMARY KEY REFERENCES hosting_operations(id),
 storage_id text NOT NULL REFERENCES hosting_storage_restore_origins(storage_id),
 plan_hash text NOT NULL,
 authority jsonb NOT NULL CHECK((jsonb_typeof(authority)='object' AND authority->>'operation_id'=operation_id AND octet_length(authority::text)<=2048) IS TRUE),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(operation_id,plan_hash) REFERENCES hosting_apply_plans(operation_id,body_hash),
 UNIQUE(operation_id,storage_id)
);
CREATE TABLE hosting_storage_restore_use_receipts (
 operation_id text PRIMARY KEY,
 storage_id text NOT NULL,
 receipt_id text NOT NULL UNIQUE CHECK(octet_length(receipt_id) BETWEEN 1 AND 128),
 authority jsonb NOT NULL CHECK(jsonb_typeof(authority)='object' AND octet_length(authority::text)<=2048),
 body jsonb NOT NULL CHECK(jsonb_typeof(body)='object' AND octet_length(body::text)<=16384),
 body_hash text NOT NULL CHECK(body_hash ~ '^sha256:[0-9a-f]{64}$'),
 audit_id text NOT NULL UNIQUE REFERENCES audit_events(id) DEFERRABLE INITIALLY DEFERRED,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(operation_id,storage_id) REFERENCES hosting_storage_restore_use_admissions(operation_id,storage_id)
);

CREATE FUNCTION nerocd_storage_archive_origin_exclusive() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 -- The shared storage row serializes cross-family admission, including inserts
 -- that otherwise lock disjoint origin tables.
 PERFORM id FROM hosting_storage WHERE id=NEW.storage_id FOR UPDATE;
 IF TG_TABLE_NAME='hosting_storage_restore_origins' THEN
  IF EXISTS(SELECT 1 FROM hosting_storage_materializations WHERE storage_id=NEW.storage_id) THEN RAISE EXCEPTION 'storage already has image origin' USING ERRCODE='23514'; END IF;
 ELSE
  IF EXISTS(SELECT 1 FROM hosting_storage_restore_origins WHERE storage_id=NEW.storage_id) THEN RAISE EXCEPTION 'storage already has restore origin' USING ERRCODE='23514'; END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER hosting_storage_archive_origin_exclusive BEFORE INSERT ON hosting_storage_restore_origins FOR EACH ROW EXECUTE FUNCTION nerocd_storage_archive_origin_exclusive();
CREATE TRIGGER hosting_storage_image_origin_exclusive BEFORE INSERT ON hosting_storage_materializations FOR EACH ROW EXECUTE FUNCTION nerocd_storage_archive_origin_exclusive();

CREATE FUNCTION nerocd_storage_archive_family_exclusive() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF EXISTS(SELECT 1 FROM hosting_storage_archive_plans WHERE operation_id=NEW.operation_id) THEN
  RAISE EXCEPTION 'archive forbids generic application evidence' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER hosting_archive_no_apply_plan BEFORE INSERT ON hosting_apply_plans FOR EACH ROW EXECUTE FUNCTION nerocd_storage_archive_family_exclusive();
CREATE TRIGGER hosting_archive_no_release BEFORE INSERT ON hosting_releases FOR EACH ROW EXECUTE FUNCTION nerocd_storage_archive_family_exclusive();
CREATE TRIGGER hosting_archive_no_generic_receipt BEFORE INSERT ON hosting_receipts FOR EACH ROW EXECUTE FUNCTION nerocd_storage_archive_family_exclusive();
CREATE TRIGGER hosting_archive_no_generic_admission BEFORE INSERT ON hosting_apply_admissions FOR EACH ROW EXECUTE FUNCTION nerocd_storage_archive_family_exclusive();
CREATE TRIGGER hosting_archive_no_generic_target BEFORE INSERT ON hosting_task_targets FOR EACH ROW EXECUTE FUNCTION nerocd_storage_archive_family_exclusive();

CREATE FUNCTION nerocd_storage_archive_consistent() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE p hosting_storage_archive_plans; o hosting_operations; src hosting_storage; dst hosting_storage;
BEGIN
 SELECT * INTO STRICT p FROM hosting_storage_archive_plans WHERE operation_id=NEW.operation_id;
 SELECT * INTO STRICT o FROM hosting_operations WHERE id=p.operation_id;
 SELECT * INTO STRICT src FROM hosting_storage WHERE id=p.source_storage_id;
 IF o.project_id<>src.project_id OR src.resource_id<>p.source_resource_id OR src.binding_id<>p.source_binding_id OR src.destination_id<>p.body->>'destination_id' OR o.project_id<>p.body->>'project_id' OR EXISTS(SELECT 1 FROM hosting_apply_plans WHERE operation_id=o.id) OR EXISTS(SELECT 1 FROM hosting_releases WHERE operation_id=o.id) OR EXISTS(SELECT 1 FROM hosting_receipts WHERE operation_id=o.id) OR EXISTS(SELECT 1 FROM hosting_task_targets WHERE operation_id=o.id) THEN RAISE EXCEPTION 'archive family or source mismatch' USING ERRCODE='23514'; END IF;
 IF p.target_resource_id IS NULL THEN
  IF o.resource_id<>p.source_resource_id OR o.binding_id<>p.source_binding_id THEN RAISE EXCEPTION 'archive source binding mismatch' USING ERRCODE='23514'; END IF;
 ELSE
  SELECT * INTO STRICT dst FROM hosting_storage WHERE id=p.target_storage_id;
  IF dst.resource_id<>p.target_resource_id OR dst.binding_id<>p.target_binding_id OR dst.project_id<>src.project_id OR dst.destination_id<>src.destination_id OR dst.mount_path<>src.mount_path OR o.resource_id<>dst.resource_id OR o.binding_id<>dst.binding_id OR p.body->'target'->'storage'->>'id'<>dst.id THEN RAISE EXCEPTION 'archive new target mismatch' USING ERRCODE='23514'; END IF;
 END IF;
 RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER hosting_storage_archive_consistency AFTER INSERT ON hosting_storage_archive_plans DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION nerocd_storage_archive_consistent();

CREATE FUNCTION nerocd_storage_archive_lease() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE t hosting_storage_archive_task_targets; spec jsonb; p hosting_storage_archive_plans;
BEGIN
 SELECT * INTO t FROM hosting_storage_archive_task_targets WHERE run_id=NEW.run_id;
 IF NOT FOUND THEN RETURN NEW; END IF;
 SELECT run_spec INTO STRICT spec FROM task_runs WHERE id=t.run_id;
 SELECT * INTO STRICT p FROM hosting_storage_archive_plans WHERE operation_id=t.operation_id;
 IF NEW.runner_id<>t.runner_id OR t.mode<>p.mode OR spec IS DISTINCT FROM jsonb_build_object('type','storage_archive','hosting_operation_id',t.operation_id,'hosting_mode',t.mode,'inputs','{}'::jsonb) THEN RAISE EXCEPTION 'archive lease target mismatch' USING ERRCODE='23514'; END IF;
 PERFORM id FROM runners WHERE id=t.runner_id AND status='active' AND token_hash<>'' AND 'storage_archive'=ANY(capabilities) AND 'hosting_storage_archive_v1'=ANY(capabilities) AND 'hosting_owned_storage_v1'=ANY(capabilities) AND NOT('shell'=ANY(capabilities)) FOR SHARE;
 IF NOT FOUND THEN RAISE EXCEPTION 'archive runner not qualified' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER hosting_storage_archive_lease BEFORE INSERT ON run_leases FOR EACH ROW EXECUTE FUNCTION nerocd_storage_archive_lease();

DO $$ DECLARE table_name text; schema_name text:=current_schema(); BEGIN
 FOREACH table_name IN ARRAY ARRAY['hosting_storage_archive_plans','hosting_storage_backups','hosting_storage_restores','hosting_storage_archive_recoveries','hosting_storage_archive_task_targets','hosting_storage_archive_receipts','hosting_storage_archive_blobs','hosting_storage_restore_origins','hosting_storage_restore_materialized','hosting_storage_restore_use_admissions','hosting_storage_restore_use_receipts'] LOOP
  EXECUTE format('CREATE TRIGGER %I BEFORE UPDATE OR DELETE ON %I FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_storage_immutable()',table_name||'_immutable',table_name);
 END LOOP;
END $$;
CREATE FUNCTION nerocd_storage_archive_schema_compatible() RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT nerocd_hosting_storage_materialization_schema_compatible() AND NOT EXISTS(SELECT 1 FROM (VALUES('hosting_storage_archive_plans'),('hosting_storage_backups'),('hosting_storage_restores'),('hosting_storage_archive_recoveries'),('hosting_storage_archive_task_targets'),('hosting_storage_archive_receipts'),('hosting_storage_archive_blobs'),('hosting_storage_restore_origins'),('hosting_storage_restore_materialized'),('hosting_storage_restore_use_admissions'),('hosting_storage_restore_use_receipts')) required(name) WHERE to_regclass(current_schema()||'.'||name) IS NULL OR NOT EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass(current_schema()||'.'||name) AND tgname=name||'_immutable' AND tgenabled IN('O','A')))
 AND NOT EXISTS(SELECT 1 FROM (VALUES
 ('hosting_storage_restore_origins','hosting_storage_archive_origin_exclusive'),
 ('hosting_storage_materializations','hosting_storage_image_origin_exclusive'),
 ('hosting_apply_plans','hosting_archive_no_apply_plan'),
 ('hosting_releases','hosting_archive_no_release'),
 ('hosting_receipts','hosting_archive_no_generic_receipt'),
 ('hosting_apply_admissions','hosting_archive_no_generic_admission'),
 ('hosting_task_targets','hosting_archive_no_generic_target'),
 ('hosting_storage_archive_plans','hosting_storage_archive_consistency'),
 ('run_leases','hosting_storage_archive_lease')) required(table_name,trigger_name)
 WHERE NOT EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass(current_schema()||'.'||table_name) AND tgname=trigger_name AND tgenabled IN('O','A')));
$$;
DO $$ DECLARE owner_schema text:=current_schema(); routine text; BEGIN
 FOREACH routine IN ARRAY ARRAY['nerocd_storage_archive_origin_exclusive()','nerocd_storage_archive_family_exclusive()','nerocd_storage_archive_consistent()','nerocd_storage_archive_lease()','nerocd_storage_archive_schema_compatible()'] LOOP
  EXECUTE format('ALTER FUNCTION %I.%s SET search_path=%I,pg_catalog,pg_temp',owner_schema,routine,owner_schema);
 END LOOP;
END $$;
