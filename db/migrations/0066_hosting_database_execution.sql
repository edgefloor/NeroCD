-- A private database writer shares fencing and storage ownership, never a host
-- port reservation, image release slot, or proxy route.
CREATE TABLE hosting_database_deployments (
 operation_id text PRIMARY KEY REFERENCES hosting_operations(id) DEFERRABLE INITIALLY DEFERRED,
 resource_id text NOT NULL REFERENCES hosting_databases(resource_id),
 binding_id text NOT NULL REFERENCES resource_bindings(id),
 destination_id text NOT NULL REFERENCES destinations(id),
 reference jsonb NOT NULL CHECK(jsonb_typeof(reference)='object' AND octet_length(reference::text)<=2048),
 credentials jsonb NOT NULL CHECK(jsonb_typeof(credentials)='array' AND jsonb_array_length(credentials)=2 AND octet_length(credentials::text)<=4096)
);
CREATE TRIGGER hosting_database_deployments_immutable BEFORE UPDATE OR DELETE ON hosting_database_deployments FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_database_immutable();

ALTER TABLE hosting_binding_state DROP CONSTRAINT hosting_binding_state_host_port_check;
ALTER TABLE hosting_binding_state ADD CONSTRAINT hosting_binding_state_host_port_check CHECK(host_port=0 OR host_port BETWEEN 1024 AND 65535);
ALTER TABLE hosting_binding_state DROP CONSTRAINT hosting_binding_state_destination_id_host_port_key;
CREATE UNIQUE INDEX hosting_binding_public_port ON hosting_binding_state(destination_id,host_port) WHERE host_port>0;

CREATE FUNCTION nerocd_hosting_database_execution_validate(operation_key text) RETURNS void LANGUAGE plpgsql AS $$
DECLARE m hosting_database_deployments; o hosting_operations; d hosting_databases; snapshot jsonb; expected_credentials jsonb;
BEGIN
 SELECT * INTO STRICT m FROM hosting_database_deployments WHERE operation_id=operation_key;
 SELECT * INTO STRICT o FROM hosting_operations WHERE id=operation_key;
 SELECT * INTO STRICT d FROM hosting_databases WHERE resource_id=m.resource_id;
 SELECT body INTO STRICT snapshot FROM hosting_snapshots WHERE operation_id=o.id;
 SELECT jsonb_agg(jsonb_build_object('role',role,'username',username,'secret_id',secret_id,'version',version) ORDER BY role) INTO expected_credentials FROM hosting_database_credentials WHERE resource_id=d.resource_id;
 IF o.resource_id<>d.resource_id OR o.project_id<>d.project_id OR o.binding_id<>d.binding_id OR m.binding_id<>d.binding_id OR m.destination_id<>d.destination_id OR m.credentials IS DISTINCT FROM expected_credentials OR m.reference->>'engine' IS DISTINCT FROM d.engine OR m.reference->>'recipe_version' IS DISTINCT FROM d.recipe_version OR (m.reference->>'configuration_hash' ~ '^sha256:[0-9a-f]{64}$') IS DISTINCT FROM true OR (SELECT count(*) FROM jsonb_object_keys(m.reference))<>3 THEN
  RAISE EXCEPTION 'database operation scope or references mismatch' USING ERRCODE='23514';
 END IF;
 IF snapshot->'database' IS DISTINCT FROM m.reference OR snapshot->>'policy_version' IS DISTINCT FROM 'database.private.owned-storage.v1' OR snapshot->>'resource_id' IS DISTINCT FROM d.resource_id OR snapshot->>'binding_id' IS DISTINCT FROM d.binding_id OR snapshot->>'destination_id' IS DISTINCT FROM d.destination_id OR snapshot->>'image_reference' IS DISTINCT FROM d.image_reference OR snapshot->'host_port' IS DISTINCT FROM '0'::jsonb OR snapshot->'container_port' IS DISTINCT FROM to_jsonb(CASE d.engine WHEN 'postgresql' THEN 5432 ELSE 6379 END) OR snapshot ?| ARRAY['local_build','runtime_configuration','storage_route_continuity'] OR snapshot->'readiness' IS DISTINCT FROM '{"path":"","timeout_seconds":0}'::jsonb THEN
  RAISE EXCEPTION 'database snapshot family mismatch' USING ERRCODE='23514';
 END IF;
 PERFORM 1 FROM resources r JOIN resource_bindings b ON b.resource_id=r.id JOIN destinations dest ON dest.id=b.destination_id JOIN servers srv ON srv.id=dest.server_id JOIN hosting_storage storage ON storage.id=d.storage_id
 WHERE r.id=d.resource_id AND r.kind='database' AND r.generation=(snapshot->>'resource_generation')::bigint AND b.id=d.binding_id AND b.destination_id=d.destination_id AND snapshot->>'runtime_namespace'=b.runtime_namespace AND snapshot->>'server_id'=srv.id AND snapshot->>'runner_id'=srv.runner_id AND snapshot->'storage'=jsonb_build_object('storage_id',storage.id,'generation',storage.generation,'hash',storage.body_hash);
 IF NOT FOUND THEN RAISE EXCEPTION 'database snapshot target or storage mismatch' USING ERRCODE='23514'; END IF;
 IF EXISTS(SELECT 1 FROM hosting_release_claims WHERE operation_id=o.id) OR EXISTS(SELECT 1 FROM hosting_release_slots WHERE binding_id=o.binding_id) THEN
  RAISE EXCEPTION 'database cannot own a public release port' USING ERRCODE='23514';
 END IF;
END;
$$;

CREATE FUNCTION nerocd_hosting_database_execution_consistent() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF EXISTS(SELECT 1 FROM hosting_database_deployments WHERE operation_id=NEW.operation_id) THEN
  PERFORM nerocd_hosting_database_execution_validate(NEW.operation_id);
 ELSIF EXISTS(SELECT 1 FROM hosting_snapshots WHERE operation_id=NEW.operation_id AND (body ? 'database' OR body->>'policy_version'='database.private.owned-storage.v1')) THEN
  RAISE EXCEPTION 'database snapshot requires explicit operation family' USING ERRCODE='23514';
 END IF;
 RETURN NULL;
END;
$$;
CREATE CONSTRAINT TRIGGER hosting_database_deployment_consistent AFTER INSERT ON hosting_database_deployments DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_database_execution_consistent();
CREATE CONSTRAINT TRIGGER hosting_database_snapshot_consistent AFTER INSERT ON hosting_snapshots DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_database_execution_consistent();

-- The preexisting generic deployment guard now requires the explicit family
-- marker, whose complete snapshot is checked at the transaction boundary.
CREATE OR REPLACE FUNCTION nerocd_hosting_database_deployment_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF EXISTS(SELECT 1 FROM resources WHERE id=NEW.resource_id AND kind='database') AND NOT EXISTS(SELECT 1 FROM hosting_database_deployments WHERE operation_id=NEW.id AND resource_id=NEW.resource_id AND binding_id=NEW.binding_id) THEN
  RAISE EXCEPTION 'database operation family is required' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END;
$$;

CREATE FUNCTION nerocd_hosting_database_private_binding() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE kind text;
BEGIN
 SELECT r.kind INTO STRICT kind FROM resources r JOIN resource_bindings b ON b.resource_id=r.id WHERE b.id=NEW.binding_id AND b.destination_id=NEW.destination_id;
 IF (kind='database') IS DISTINCT FROM (NEW.host_port=0) THEN RAISE EXCEPTION 'private database binding port mismatch' USING ERRCODE='23514'; END IF;
 IF kind='database' AND NOT EXISTS(SELECT 1 FROM hosting_databases WHERE binding_id=NEW.binding_id AND destination_id=NEW.destination_id) THEN RAISE EXCEPTION 'database metadata missing' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER hosting_database_private_binding BEFORE INSERT ON hosting_binding_state FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_database_private_binding();

CREATE FUNCTION nerocd_hosting_database_public_port_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF EXISTS(SELECT 1 FROM resources r JOIN resource_bindings b ON b.resource_id=r.id WHERE b.id=NEW.binding_id AND r.kind='database') THEN
  RAISE EXCEPTION 'database cannot own a public port ledger' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER hosting_database_public_slot_guard BEFORE INSERT ON hosting_release_slots FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_database_public_port_guard();
CREATE TRIGGER hosting_database_public_claim_guard BEFORE INSERT ON hosting_release_claims FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_database_public_port_guard();
CREATE TRIGGER hosting_database_public_release_guard BEFORE INSERT ON hosting_retained_releases FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_database_public_port_guard();

-- Existing image/runtime/artifact checks below are retained verbatim after the
-- guarded database branch. Applied migration 0063 is unchanged.
CREATE OR REPLACE FUNCTION nerocd_hosting_build_deployment_consistent() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE op_key text; snap jsonb; rel jsonb; artifact jsonb; artifact_hash text; build_row hosting_builds; op_row hosting_operations; association hosting_operation_build_artifacts;
BEGIN
 op_key:=NEW.operation_id;
 SELECT * INTO op_row FROM hosting_operations WHERE id=op_key;
 SELECT body INTO snap FROM hosting_snapshots WHERE operation_id=op_key;
 IF snap ? 'database' OR snap->>'policy_version'='database.private.owned-storage.v1' THEN
  PERFORM nerocd_hosting_database_execution_validate(op_key);
  IF EXISTS(SELECT 1 FROM hosting_operation_build_artifacts WHERE operation_id=op_key) THEN RAISE EXCEPTION 'database cannot bind local build artifact' USING ERRCODE='23514'; END IF;
  RETURN NULL;
 END IF;
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

CREATE OR REPLACE FUNCTION nerocd_hosting_storage_lease() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE snap jsonb; mode text;
BEGIN
 SELECT sn.body,t.mode INTO snap,mode FROM hosting_task_targets t JOIN hosting_snapshots sn ON sn.operation_id=t.operation_id WHERE t.run_id=NEW.run_id;
 IF snap ? 'database' THEN
  PERFORM 1 FROM hosting_task_targets t JOIN task_runs tr ON tr.id=t.run_id JOIN hosting_operations op ON op.id=t.operation_id
  WHERE t.run_id=NEW.run_id AND t.runner_id=NEW.runner_id AND t.binding_id=snap->>'binding_id' AND NEW.runner_id=snap->>'runner_id' AND tr.project_id=op.project_id AND t.mode IN ('deploy','observe') AND tr.run_spec->>'type'='database_deploy' AND tr.run_spec->>'hosting_operation_id'=t.operation_id AND tr.run_spec->>'hosting_mode'=t.mode AND tr.run_spec - 'type' - 'hosting_operation_id' - 'hosting_mode' - 'inputs'='{}'::jsonb AND coalesce(tr.run_spec->'inputs','{}'::jsonb)='{}'::jsonb AND tr.workflow='{}'::jsonb;
  IF NOT FOUND THEN RAISE EXCEPTION 'database lease requires exact typed target' USING ERRCODE='23514'; END IF;
  PERFORM nerocd_hosting_database_execution_validate((SELECT operation_id FROM hosting_task_targets WHERE run_id=NEW.run_id));
  PERFORM id FROM runners WHERE id=NEW.runner_id AND status='active' AND token_hash<>'' AND 'hosting_owned_storage_v1'=ANY(capabilities) AND 'hosting_database_v1'=ANY(capabilities) AND 'database_deploy'=ANY(capabilities) AND NOT('shell'=ANY(capabilities)) FOR SHARE;
  IF NOT FOUND THEN RAISE EXCEPTION 'database requires explicit private database and storage capabilities' USING ERRCODE='23514'; END IF;
 ELSIF mode='deploy' AND snap ? 'storage' THEN
  PERFORM id FROM runners WHERE id=NEW.runner_id AND status='active' AND token_hash<>'' AND 'hosting_owned_storage_v1'=ANY(capabilities) AND 'image_release_ports_v1'=ANY(capabilities) FOR SHARE;
  IF NOT FOUND THEN RAISE EXCEPTION 'storage deployment requires explicit storage and release port capabilities' USING ERRCODE='23514'; END IF;
 END IF;
 RETURN NEW;
END $$;

CREATE FUNCTION nerocd_hosting_database_execution_schema_compatible() RETURNS boolean LANGUAGE sql STABLE AS $$
SELECT to_regclass(current_schema()||'.hosting_database_deployments') IS NOT NULL
AND NOT EXISTS(SELECT 1 FROM (VALUES ('hosting_database_deployments','hosting_database_deployments_immutable'),('hosting_database_deployments','hosting_database_deployment_consistent'),('hosting_snapshots','hosting_database_snapshot_consistent'),('hosting_release_slots','hosting_database_public_slot_guard'),('hosting_release_claims','hosting_database_public_claim_guard'),('hosting_retained_releases','hosting_database_public_release_guard'),('hosting_binding_state','hosting_database_private_binding'),('hosting_operations','hosting_database_deployment_guard')) required(table_name,trigger_name) WHERE NOT EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass(current_schema()||'.'||required.table_name) AND tgname=required.trigger_name AND tgenabled IN ('O','A')))
AND EXISTS(SELECT 1 FROM pg_index WHERE indexrelid=to_regclass(current_schema()||'.hosting_binding_public_port') AND indisunique AND indisvalid);
$$;

DO $$ DECLARE owner_schema text:=current_schema(); routine text; BEGIN
 FOREACH routine IN ARRAY ARRAY['nerocd_hosting_database_execution_validate(text)','nerocd_hosting_database_execution_consistent()','nerocd_hosting_database_deployment_guard()','nerocd_hosting_database_private_binding()','nerocd_hosting_database_public_port_guard()','nerocd_hosting_database_execution_schema_compatible()'] LOOP
  EXECUTE format('ALTER FUNCTION %I.%s SET search_path=%I,pg_catalog,pg_temp',owner_schema,routine,owner_schema);
 END LOOP;
END $$;

DO $$ DECLARE owner_schema text:=current_schema(); BEGIN
 EXECUTE format('ALTER FUNCTION %I.nerocd_hosting_build_deployment_consistent() SET search_path TO pg_catalog, %I, pg_temp',owner_schema,owner_schema);
 EXECUTE format('ALTER FUNCTION %I.nerocd_hosting_storage_lease() SET search_path TO pg_catalog, %I, pg_temp',owner_schema,owner_schema);
END $$;
