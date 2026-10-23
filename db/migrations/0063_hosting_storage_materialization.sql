-- Irreversible creation history is independent of workload success and cleanup.
ALTER TABLE hosting_apply_plans ADD CONSTRAINT hosting_apply_plans_storage_hash UNIQUE(operation_id,body_hash);
CREATE TABLE hosting_storage_materializations (
 storage_id text PRIMARY KEY REFERENCES hosting_storage(id) ON DELETE RESTRICT,
 origin_operation_id text NOT NULL,
 origin_plan_hash text NOT NULL,
 origin jsonb NOT NULL CHECK((jsonb_typeof(origin)='object' AND octet_length(origin::text)<=16384 AND origin->>'state'='create_admitted' AND origin->'storage_ref'->>'storage_id'=storage_id AND origin->>'creation_operation_id'=origin_operation_id AND origin->>'creation_plan_hash'=origin_plan_hash) IS TRUE),
 origin_hash text NOT NULL CHECK(origin_hash ~ '^sha256:[0-9a-f]{64}$'),
 materialized_operation_id text,
 materialized_receipt_id text,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(origin_operation_id,origin_plan_hash) REFERENCES hosting_apply_plans(operation_id,body_hash) ON DELETE RESTRICT,
 CHECK((materialized_operation_id IS NULL)=(materialized_receipt_id IS NULL))
);
CREATE TABLE hosting_storage_operation_admissions (
 operation_id text PRIMARY KEY REFERENCES hosting_operations(id) ON DELETE RESTRICT,
 storage_id text NOT NULL REFERENCES hosting_storage_materializations(storage_id) ON DELETE RESTRICT,
 plan_hash text NOT NULL,
 authority jsonb NOT NULL CHECK(jsonb_typeof(authority)='object' AND octet_length(authority::text)<=2048 AND authority->>'operation_id'=operation_id),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(operation_id,plan_hash) REFERENCES hosting_apply_plans(operation_id,body_hash) ON DELETE RESTRICT,
 UNIQUE(operation_id,storage_id)
);
CREATE TABLE hosting_storage_materialized_receipts (
 operation_id text PRIMARY KEY,
 storage_id text NOT NULL,
 receipt_id text NOT NULL CHECK(octet_length(receipt_id) BETWEEN 1 AND 128),
 authority jsonb NOT NULL CHECK(jsonb_typeof(authority)='object' AND octet_length(authority::text)<=2048),
 body jsonb NOT NULL CHECK(jsonb_typeof(body)='object' AND octet_length(body::text)<=16384),
 body_hash text NOT NULL CHECK(body_hash ~ '^sha256:[0-9a-f]{64}$'),
 audit_id text NOT NULL UNIQUE REFERENCES audit_events(id) DEFERRABLE INITIALLY DEFERRED,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(operation_id,storage_id) REFERENCES hosting_storage_operation_admissions(operation_id,storage_id) ON DELETE RESTRICT,
 UNIQUE(operation_id,receipt_id),
 UNIQUE(storage_id,operation_id,receipt_id)
);
ALTER TABLE hosting_storage_materializations ADD CONSTRAINT hosting_storage_materialized_receipt_fk
 FOREIGN KEY(storage_id,materialized_operation_id,materialized_receipt_id) REFERENCES hosting_storage_materialized_receipts(storage_id,operation_id,receipt_id) DEFERRABLE INITIALLY DEFERRED;

ALTER TABLE hosting_storage_materializations ADD CONSTRAINT hosting_storage_origin_admission_fk
 FOREIGN KEY(origin_operation_id,storage_id) REFERENCES hosting_storage_operation_admissions(operation_id,storage_id) DEFERRABLE INITIALLY DEFERRED;

CREATE FUNCTION nerocd_hosting_storage_materialization_transition() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' OR NEW.storage_id IS DISTINCT FROM OLD.storage_id OR NEW.origin_operation_id IS DISTINCT FROM OLD.origin_operation_id OR NEW.origin_plan_hash IS DISTINCT FROM OLD.origin_plan_hash OR NEW.origin IS DISTINCT FROM OLD.origin OR NEW.origin_hash IS DISTINCT FROM OLD.origin_hash OR NEW.created_at IS DISTINCT FROM OLD.created_at OR OLD.materialized_operation_id IS NOT NULL OR NEW.materialized_operation_id IS NULL OR NEW.materialized_receipt_id IS NULL THEN
  RAISE EXCEPTION 'storage creation history is irreversible' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER hosting_storage_materialization_transition BEFORE UPDATE OR DELETE ON hosting_storage_materializations FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_storage_materialization_transition();
CREATE TRIGGER hosting_storage_operation_admissions_immutable BEFORE UPDATE OR DELETE ON hosting_storage_operation_admissions FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_storage_immutable();
CREATE TRIGGER hosting_storage_materialized_receipts_immutable BEFORE UPDATE OR DELETE ON hosting_storage_materialized_receipts FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_storage_immutable();

CREATE FUNCTION nerocd_hosting_storage_admission_consistent() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE origin hosting_storage_materializations%ROWTYPE; storage hosting_storage%ROWTYPE; op hosting_operations%ROWTYPE; plan jsonb; binding_hold text;
BEGIN
 SELECT * INTO STRICT origin FROM hosting_storage_materializations WHERE storage_id=NEW.storage_id;
 SELECT * INTO STRICT storage FROM hosting_storage WHERE id=NEW.storage_id;
 SELECT * INTO STRICT op FROM hosting_operations WHERE id=NEW.operation_id;
 SELECT body INTO STRICT plan FROM hosting_apply_plans WHERE operation_id=op.id;
 SELECT hold_operation_id INTO binding_hold FROM hosting_binding_state WHERE binding_id=op.binding_id;
 IF op.resource_id<>storage.resource_id OR op.project_id<>storage.project_id OR op.binding_id<>storage.binding_id OR binding_hold IS DISTINCT FROM op.id OR NEW.authority->>'run_id' IS DISTINCT FROM op.run_id OR NEW.authority->>'operation_id' IS DISTINCT FROM op.id OR plan->'storage'->'reference' IS DISTINCT FROM origin.origin->'storage_ref' OR plan->'storage'->>'creation_operation_id' IS DISTINCT FROM origin.origin_operation_id OR plan->'storage'->>'creation_nonce' IS DISTINCT FROM origin.origin->>'creation_nonce' OR plan->'release'->>'daemon_id' IS DISTINCT FROM origin.origin->>'daemon_id' THEN
  RAISE EXCEPTION 'storage admission target mismatch' USING ERRCODE='23514';
 END IF;
 RETURN NULL;
END;
$$;
CREATE CONSTRAINT TRIGGER hosting_storage_admission_consistency AFTER INSERT ON hosting_storage_operation_admissions DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_storage_admission_consistent();

CREATE FUNCTION nerocd_hosting_storage_receipt_consistent() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE admission hosting_storage_operation_admissions%ROWTYPE; origin hosting_storage_materializations%ROWTYPE;
BEGIN
 SELECT * INTO STRICT admission FROM hosting_storage_operation_admissions WHERE operation_id=NEW.operation_id;
 SELECT * INTO STRICT origin FROM hosting_storage_materializations WHERE storage_id=NEW.storage_id;
 IF NEW.authority IS DISTINCT FROM admission.authority OR NOT (NEW.body @> NEW.authority) OR NEW.body->>'plan_hash' IS DISTINCT FROM admission.plan_hash OR NEW.body->'storage_ref' IS DISTINCT FROM origin.origin->'storage_ref' OR NEW.body->>'creation_plan_hash' IS DISTINCT FROM origin.origin_plan_hash OR NEW.body->'labels' IS DISTINCT FROM origin.origin->'labels' OR NEW.body->>'driver' IS DISTINCT FROM 'local' OR NEW.body->>'scope' IS DISTINCT FROM 'local' OR coalesce(NEW.body->'options','{}'::jsonb) NOT IN ('{}'::jsonb,'null'::jsonb) OR origin.materialized_operation_id IS NULL THEN
  RAISE EXCEPTION 'storage receipt mismatch' USING ERRCODE='23514';
 END IF;
 IF NOT EXISTS(SELECT 1 FROM audit_events a WHERE a.id=NEW.audit_id AND a.action='hosting.storage.materialized' AND a.target_id=NEW.storage_id AND a.metadata->>'operation_id'=NEW.operation_id AND a.metadata->>'plan_hash'=admission.plan_hash) THEN
  RAISE EXCEPTION 'storage materialization audit mismatch' USING ERRCODE='23514';
 END IF;
 RETURN NULL;
END;
$$;
CREATE CONSTRAINT TRIGGER hosting_storage_materialized_receipt_consistency AFTER INSERT ON hosting_storage_materialized_receipts DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_storage_receipt_consistent();

CREATE FUNCTION nerocd_hosting_storage_materialization_schema_compatible() RETURNS boolean LANGUAGE sql STABLE AS $$
SELECT nerocd_hosting_storage_schema_compatible()
AND to_regclass(current_schema()||'.hosting_storage_materializations') IS NOT NULL
AND to_regclass(current_schema()||'.hosting_storage_operation_admissions') IS NOT NULL
AND to_regclass(current_schema()||'.hosting_storage_materialized_receipts') IS NOT NULL
AND EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid=to_regclass(current_schema()||'.hosting_storage_materializations') AND conname='hosting_storage_materialized_receipt_fk' AND convalidated AND contype='f')
AND EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid=to_regclass(current_schema()||'.hosting_storage_materializations') AND conname='hosting_storage_origin_admission_fk' AND convalidated AND contype='f')
AND NOT EXISTS(SELECT 1 FROM (VALUES ('run_leases','hosting_storage_lease'),('hosting_storage_materializations','hosting_storage_materialization_transition'),('hosting_storage_operation_admissions','hosting_storage_operation_admissions_immutable'),('hosting_storage_operation_admissions','hosting_storage_admission_consistency'),('hosting_storage_materialized_receipts','hosting_storage_materialized_receipts_immutable'),('hosting_storage_materialized_receipts','hosting_storage_materialized_receipt_consistency')) required(table_name,trigger_name) WHERE NOT EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass(current_schema()||'.'||required.table_name) AND tgname=required.trigger_name AND tgenabled IN('O','A')));
$$;
DO $$ DECLARE owner_schema text:=current_schema(); routine text; BEGIN
 FOREACH routine IN ARRAY ARRAY['nerocd_hosting_storage_materialization_transition()','nerocd_hosting_storage_admission_consistent()','nerocd_hosting_storage_receipt_consistent()','nerocd_hosting_storage_materialization_schema_compatible()'] LOOP
  EXECUTE format('ALTER FUNCTION %I.%s SET search_path=%I,pg_catalog,pg_temp',owner_schema,routine,owner_schema);
 END LOOP;
END $$;

-- Preserve existing reference/artifact checks while admitting one new explicit family.
CREATE OR REPLACE FUNCTION nerocd_hosting_build_deployment_consistent() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE op_key text; snap jsonb; rel jsonb; artifact jsonb; artifact_hash text; build_row hosting_builds; op_row hosting_operations; association hosting_operation_build_artifacts;
BEGIN
 op_key:=NEW.operation_id;
 SELECT * INTO op_row FROM hosting_operations WHERE id=op_key;
 SELECT body INTO snap FROM hosting_snapshots WHERE operation_id=op_key;
 -- Preserve historical rows while binding every newly inserted snapshot to
 -- its immutable runtime document. Current generation and secret liveness are
 -- fresh-admission checks, not historical evidence constraints.
 IF snap ? 'storage' THEN
  IF snap ? 'runtime_configuration' OR snap ? 'local_build' OR snap->>'policy_version' IS DISTINCT FROM 'public-image.owned-storage.release-ports.v1'
  OR NOT EXISTS(SELECT 1 FROM hosting_storage st WHERE st.id=snap->'storage'->>'storage_id' AND st.generation=(snap->'storage'->>'generation')::bigint AND st.body_hash=snap->'storage'->>'hash' AND st.resource_id=op_row.resource_id AND st.project_id=op_row.project_id AND st.binding_id=op_row.binding_id)
  THEN RAISE EXCEPTION 'storage snapshot identity mismatch' USING ERRCODE='23514'; END IF;
 ELSIF snap->>'policy_version'='public-image.owned-storage.release-ports.v1' THEN
  RAISE EXCEPTION 'storage policy requires storage reference' USING ERRCODE='23514';
 ELSIF snap ? 'runtime_configuration' THEN
  IF jsonb_typeof(snap->'runtime_configuration') IS DISTINCT FROM 'object'
  OR ((snap->'runtime_configuration') - 'generation' - 'hash') <> '{}'::jsonb
  OR jsonb_typeof(snap->'runtime_configuration'->'generation') IS DISTINCT FROM 'number'
  OR coalesce(snap->'runtime_configuration'->>'generation','') !~ '^[1-9][0-9]{0,18}$'
  OR coalesce(snap->'runtime_configuration'->>'hash','') !~ '^sha256:[0-9a-f]{64}$' THEN
   RAISE EXCEPTION 'invalid runtime configuration reference' USING ERRCODE='23514';
  END IF;
  IF (snap->'runtime_configuration'->>'generation')::numeric > 9223372036854775807
  OR snap->>'policy_version' IS DISTINCT FROM (CASE WHEN snap ? 'local_build' THEN 'local-build.runtime-env.release-ports.v1' ELSE 'public-image.runtime-env.release-ports.v1' END)
  OR NOT EXISTS(SELECT 1 FROM hosting_runtime_configuration_versions v WHERE v.resource_id=op_row.resource_id AND v.project_id=op_row.project_id AND v.generation=(snap->'runtime_configuration'->>'generation')::numeric AND v.body_hash=snap->'runtime_configuration'->>'hash') THEN
   RAISE EXCEPTION 'runtime configuration reference mismatch' USING ERRCODE='23514';
  END IF;
 ELSE
  IF (snap ? 'local_build' AND snap->>'policy_version' IS DISTINCT FROM 'local-build.release-ports.v1')
  OR (NOT (snap ? 'local_build') AND coalesce(snap->>'policy_version','') NOT IN ('public-image.loopback.v1','public-image.release-ports.v1')) THEN
   RAISE EXCEPTION 'hosting policy requires matching runtime reference' USING ERRCODE='23514';
  END IF;
 END IF;
 SELECT * INTO association FROM hosting_operation_build_artifacts WHERE operation_id=op_key;
 IF NOT FOUND THEN
  IF snap ? 'local_build' THEN RAISE EXCEPTION 'local snapshot requires artifact association' USING ERRCODE='23514'; END IF;
  SELECT body INTO rel FROM hosting_releases WHERE operation_id=op_key;
  IF rel ? 'local_build' THEN RAISE EXCEPTION 'local release requires artifact association' USING ERRCODE='23514'; END IF;
  RETURN NULL;
 END IF;
 SELECT * INTO build_row FROM hosting_builds WHERE id=association.build_id;
 SELECT body,body_hash INTO artifact,artifact_hash FROM hosting_build_artifacts WHERE build_id=association.build_id;
 IF build_row.state<>'succeeded' OR build_row.resource_id<>op_row.resource_id OR build_row.project_id<>op_row.project_id
 OR (snap->'local_build'->'artifact') IS DISTINCT FROM artifact OR snap->'local_build'->>'artifact_hash' IS DISTINCT FROM artifact_hash
 OR (snap->'local_build'->>'source_generation')::bigint IS DISTINCT FROM (build_row.snapshot->'source'->>'generation')::bigint
 OR snap->>'image_reference' IS DISTINCT FROM ''
 OR (snap->>'resource_generation')::bigint IS DISTINCT FROM (build_row.snapshot->>'resource_generation')::bigint
 OR snap->>'resource_id' IS DISTINCT FROM op_row.resource_id OR snap->>'binding_id' IS DISTINCT FROM op_row.binding_id
 OR snap->>'binding_id' IS DISTINCT FROM build_row.snapshot->>'binding_id'
 OR snap->>'destination_id' IS DISTINCT FROM build_row.snapshot->>'destination_id'
 OR snap->>'server_id' IS DISTINCT FROM build_row.snapshot->>'server_id'
 OR snap->>'runner_id' IS DISTINCT FROM build_row.snapshot->>'runner_id' THEN
  RAISE EXCEPTION 'local deployment does not match successful artifact target' USING ERRCODE='23514';
 END IF;
 SELECT body INTO rel FROM hosting_releases WHERE operation_id=op_key;
 IF FOUND AND ((rel->'local_build') IS DISTINCT FROM artifact OR rel->>'canonical_image' IS DISTINCT FROM ''
 OR rel->>'image_id' IS DISTINCT FROM artifact->>'image_id' OR rel->>'daemon_id' IS DISTINCT FROM artifact->>'daemon_id'
 OR rel->>'os' IS DISTINCT FROM split_part(artifact->>'platform','/',1) OR rel->>'architecture' IS DISTINCT FROM split_part(artifact->>'platform','/',2)
 OR coalesce(rel->>'variant','')<>'') THEN RAISE EXCEPTION 'local release substitution' USING ERRCODE='23514'; END IF;
 RETURN NULL;
END;$$;

DO $$ DECLARE owner_schema text:=current_schema(); BEGIN
 EXECUTE format('ALTER FUNCTION %I.nerocd_hosting_build_deployment_consistent() SET search_path TO pg_catalog, %I, pg_temp',owner_schema,owner_schema);
END $$;

CREATE FUNCTION nerocd_hosting_storage_lease() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE snap jsonb; mode text;
BEGIN
 SELECT sn.body,t.mode INTO snap,mode FROM hosting_task_targets t JOIN hosting_snapshots sn ON sn.operation_id=t.operation_id WHERE t.run_id=NEW.run_id;
 IF mode='deploy' AND snap ? 'storage' THEN
  PERFORM id FROM runners WHERE id=NEW.runner_id AND status='active' AND token_hash<>'' AND 'hosting_owned_storage_v1'=ANY(capabilities) AND 'image_release_ports_v1'=ANY(capabilities) FOR SHARE;
  IF NOT FOUND THEN RAISE EXCEPTION 'storage deployment requires explicit storage and release port capabilities' USING ERRCODE='23514'; END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER hosting_storage_lease BEFORE INSERT ON run_leases FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_storage_lease();
DO $$ DECLARE owner_schema text:=current_schema(); BEGIN
 EXECUTE format('ALTER FUNCTION %I.nerocd_hosting_storage_lease() SET search_path TO pg_catalog, %I, pg_temp',owner_schema,owner_schema);
END $$;
