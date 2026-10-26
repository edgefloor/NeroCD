CREATE TABLE hosting_catalog_installations (
 project_id text NOT NULL REFERENCES projects(id),
 request_id text NOT NULL CHECK (request_id ~ '^[0-9a-f]{32}$'),
 resource_id text NOT NULL UNIQUE REFERENCES resources(id),
 actor_id text NOT NULL,
 request_hash text NOT NULL CHECK (request_hash ~ '^sha256:[0-9a-f]{64}$'),
 body jsonb NOT NULL,
 body_hash text NOT NULL CHECK (body_hash ~ '^sha256:[0-9a-f]{64}$'),
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(project_id,request_id)
);

CREATE TRIGGER hosting_catalog_installations_immutable BEFORE UPDATE OR DELETE ON hosting_catalog_installations FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_runtime_immutable();

CREATE OR REPLACE FUNCTION nerocd_hosting_build_deployment_consistent() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE op_key text; snap jsonb; rel jsonb; artifact jsonb; artifact_hash text; build_row hosting_builds; op_row hosting_operations; association hosting_operation_build_artifacts;
BEGIN
 op_key:=NEW.operation_id;
 SELECT * INTO op_row FROM hosting_operations WHERE id=op_key;
 SELECT body INTO snap FROM hosting_snapshots WHERE operation_id=op_key;
 IF EXISTS(SELECT 1 FROM hosting_database_connection_executions WHERE operation_id=op_key AND family='application') THEN
  PERFORM nerocd_database_connection_validate(op_key,false);
  IF EXISTS(SELECT 1 FROM hosting_operation_build_artifacts WHERE operation_id=op_key) THEN RAISE EXCEPTION 'connected application cannot bind local artifact' USING ERRCODE='23514'; END IF;
  RETURN NULL;
 END IF;
 IF snap ? 'database' OR snap->>'policy_version'='database.private.owned-storage.v1' THEN
  PERFORM nerocd_hosting_database_execution_validate(op_key);
  IF EXISTS(SELECT 1 FROM hosting_operation_build_artifacts WHERE operation_id=op_key) THEN RAISE EXCEPTION 'database cannot bind local build artifact' USING ERRCODE='23514'; END IF;
  RETURN NULL;
 END IF;
 -- Preserve historical rows while binding every newly inserted snapshot to
 -- its immutable runtime document. Current generation and secret liveness are
 -- fresh-admission checks, not historical evidence constraints.
 IF snap->>'policy_version'='public-image.owned-storage.runtime-env.release-ports.v1' THEN
  IF snap ? 'local_build' OR snap ? 'application_database' OR snap ? 'database'
  OR jsonb_typeof(snap->'runtime_configuration') IS DISTINCT FROM 'object'
  OR ((snap->'runtime_configuration') - 'generation' - 'hash') <> '{}'::jsonb
  OR jsonb_typeof(snap->'runtime_configuration'->'generation') IS DISTINCT FROM 'number'
  OR coalesce(snap->'runtime_configuration'->>'generation','') !~ '^[1-9][0-9]{0,18}$'
  OR coalesce(snap->'runtime_configuration'->>'hash','') !~ '^sha256:[0-9a-f]{64}$'
  OR (snap->'runtime_configuration'->>'generation')::numeric > 9223372036854775807
  OR NOT EXISTS(SELECT 1 FROM hosting_runtime_configuration_versions v WHERE v.resource_id=op_row.resource_id AND v.project_id=op_row.project_id AND v.generation=(snap->'runtime_configuration'->>'generation')::numeric AND v.body_hash=snap->'runtime_configuration'->>'hash')
  OR NOT EXISTS(SELECT 1 FROM hosting_storage st WHERE st.id=snap->'storage'->>'storage_id' AND st.generation=(snap->'storage'->>'generation')::bigint AND st.body_hash=snap->'storage'->>'hash' AND st.resource_id=op_row.resource_id AND st.project_id=op_row.project_id AND st.binding_id=op_row.binding_id)
  THEN RAISE EXCEPTION 'owned storage runtime snapshot mismatch' USING ERRCODE='23514'; END IF;
 ELSIF snap ? 'storage' THEN
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

CREATE FUNCTION nerocd_owned_storage_runtime_lease() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE snap jsonb; mode text;
BEGIN
 SELECT sn.body,t.mode INTO snap,mode FROM hosting_task_targets t JOIN hosting_snapshots sn ON sn.operation_id=t.operation_id WHERE t.run_id=NEW.run_id;
 IF mode='deploy' AND snap->>'policy_version'='public-image.owned-storage.runtime-env.release-ports.v1' THEN
 PERFORM id FROM runners WHERE id=NEW.runner_id AND status='active' AND token_hash<>'' AND 'hosting_owned_storage_runtime_v1'=ANY(capabilities) AND 'hosting_runtime_env_v1'=ANY(capabilities) AND 'hosting_owned_storage_v1'=ANY(capabilities) AND 'image_release_ports_v1'=ANY(capabilities) AND 'image_deploy'=ANY(capabilities) AND NOT('shell'=ANY(capabilities)) FOR SHARE;
 IF NOT FOUND THEN RAISE EXCEPTION 'owned storage runtime requires explicit compatible runner' USING ERRCODE='23514'; END IF;
 END IF;RETURN NEW;
END $$;
CREATE TRIGGER hosting_owned_storage_runtime_lease BEFORE INSERT ON run_leases FOR EACH ROW EXECUTE FUNCTION nerocd_owned_storage_runtime_lease();
DO $$ DECLARE owner_schema text:=current_schema(); BEGIN
 EXECUTE format('ALTER FUNCTION %I.nerocd_hosting_build_deployment_consistent() SET search_path TO pg_catalog, %I, pg_temp',owner_schema,owner_schema);
 EXECUTE format('ALTER FUNCTION %I.nerocd_owned_storage_runtime_lease() SET search_path=%I,pg_catalog,pg_temp',owner_schema,owner_schema);
END $$;
