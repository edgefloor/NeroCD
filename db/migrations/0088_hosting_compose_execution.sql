-- Volume creation is admitted with the existing binding mutation hold. Objects
-- remain retained independently of later source revisions and component removal.
CREATE TABLE hosting_compose_volumes (
 binding_id text NOT NULL REFERENCES resource_bindings(id) ON DELETE RESTRICT,
 name text NOT NULL CHECK(name ~ '^[a-z][a-z0-9_-]{0,62}$'),
 volume_name text NOT NULL UNIQUE CHECK(volume_name ~ '^ncd_[0-9a-f]{32}$'),
 destination_id text NOT NULL REFERENCES destinations(id) ON DELETE RESTRICT,
 daemon_id text NOT NULL CHECK(octet_length(daemon_id) BETWEEN 1 AND 128),
 creation_operation_id text NOT NULL REFERENCES hosting_operations(id) ON DELETE RESTRICT,
 creation_plan_hash text NOT NULL CHECK(creation_plan_hash ~ '^sha256:[0-9a-f]{64}$'),
 creation_nonce text NOT NULL CHECK(creation_nonce ~ '^[0-9a-f]{32}$'),
 state text NOT NULL CHECK(state IN('admitted','materialized')),
 created_at text NOT NULL DEFAULT '' CHECK(octet_length(created_at)<=128),
 PRIMARY KEY(binding_id,name),
 CHECK((state='admitted' AND created_at='') OR (state='materialized' AND created_at<>''))
);

CREATE FUNCTION nerocd_hosting_compose_volume_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE target hosting_operations; snap jsonb; plan jsonb;
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'stack volumes are retained' USING ERRCODE='23514'; END IF;
 IF TG_OP='UPDATE' THEN
  IF (NEW.binding_id,NEW.name,NEW.volume_name,NEW.destination_id,NEW.daemon_id,NEW.creation_operation_id,NEW.creation_plan_hash,NEW.creation_nonce)
   IS DISTINCT FROM (OLD.binding_id,OLD.name,OLD.volume_name,OLD.destination_id,OLD.daemon_id,OLD.creation_operation_id,OLD.creation_plan_hash,OLD.creation_nonce)
   OR (OLD.state='materialized' AND NEW IS DISTINCT FROM OLD)
   OR (OLD.state='admitted' AND NEW.state<>'materialized') THEN RAISE EXCEPTION 'stack volume origin is immutable' USING ERRCODE='23514'; END IF;
  RETURN NEW;
 END IF;
 SELECT * INTO target FROM hosting_operations WHERE id=NEW.creation_operation_id;
 SELECT body INTO snap FROM hosting_snapshots WHERE operation_id=target.id;
 SELECT body INTO plan FROM hosting_apply_plans WHERE operation_id=target.id AND body_hash=NEW.creation_plan_hash;
 IF target.binding_id IS DISTINCT FROM NEW.binding_id OR target.state IS DISTINCT FROM 'applying'
 OR snap->>'policy_version' IS DISTINCT FROM 'managed-compose.interrupting.v1'
 OR snap->>'destination_id' IS DISTINCT FROM NEW.destination_id
 OR plan->'release'->>'daemon_id' IS DISTINCT FROM NEW.daemon_id
 OR NOT EXISTS(SELECT 1 FROM hosting_binding_state WHERE binding_id=NEW.binding_id AND hold_operation_id=target.id)
 OR NOT EXISTS(SELECT 1 FROM jsonb_array_elements(plan->'stack'->'volumes') v
  WHERE v->>'name'=NEW.name AND v->>'volume_name'=NEW.volume_name AND v->>'creation_operation_id'=NEW.creation_operation_id AND v->>'creation_nonce'=NEW.creation_nonce AND coalesce(v->>'created_at','')='')
 OR NEW.state IS DISTINCT FROM 'admitted' OR NEW.created_at<>'' THEN RAISE EXCEPTION 'stack volume requires exact mutation admission' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END;$$;
CREATE TRIGGER hosting_compose_volume_guard BEFORE INSERT OR UPDATE OR DELETE ON hosting_compose_volumes FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_compose_volume_guard();

CREATE FUNCTION nerocd_hosting_compose_lease() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE snap jsonb; target hosting_task_targets; spec jsonb;
BEGIN
 SELECT * INTO target FROM hosting_task_targets WHERE run_id=NEW.run_id;
 SELECT body INTO snap FROM hosting_snapshots WHERE operation_id=target.operation_id;
 IF snap ? 'stack' THEN
  SELECT run_spec INTO spec FROM task_runs WHERE id=NEW.run_id;
  IF target.runner_id IS DISTINCT FROM NEW.runner_id OR target.mode NOT IN('deploy','observe')
   OR spec->>'type' IS DISTINCT FROM 'image_deploy' OR spec->>'hosting_operation_id' IS DISTINCT FROM target.operation_id
   OR spec->>'hosting_mode' IS DISTINCT FROM target.mode OR spec-'type'-'hosting_operation_id'-'hosting_mode'-'inputs'<>'{}'::jsonb
   OR coalesce(spec->'inputs','{}'::jsonb)<>'{}'::jsonb THEN RAISE EXCEPTION 'invalid managed stack task' USING ERRCODE='23514'; END IF;
  PERFORM id FROM runners WHERE id=NEW.runner_id AND status='active' AND token_hash<>''
   AND capabilities @> ARRAY['hosting_managed_compose_v1','hosting_runtime_env_v1','image_release_ports_v1','image_deploy']::text[] AND NOT('shell'=ANY(capabilities)) FOR SHARE;
  IF NOT FOUND THEN RAISE EXCEPTION 'managed stack requires versioned configured agent' USING ERRCODE='23514'; END IF;
 END IF;
 RETURN NEW;
END;$$;
CREATE TRIGGER hosting_compose_lease BEFORE INSERT ON run_leases FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_compose_lease();

CREATE OR REPLACE FUNCTION nerocd_hosting_build_deployment_consistent() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE op_key text; snap jsonb; rel jsonb; artifact jsonb; artifact_hash text; build_row hosting_builds; op_row hosting_operations; association hosting_operation_build_artifacts; stack_plan jsonb; stack_source jsonb;
BEGIN
 op_key:=NEW.operation_id;
 SELECT * INTO op_row FROM hosting_operations WHERE id=op_key;
 SELECT body INTO snap FROM hosting_snapshots WHERE operation_id=op_key;
 SELECT body INTO rel FROM hosting_releases WHERE operation_id=op_key;
 SELECT body INTO stack_plan FROM hosting_apply_plans WHERE operation_id=op_key;
 IF snap ? 'stack' OR snap->>'policy_version'='managed-compose.interrupting.v1' THEN
  IF snap->>'policy_version' IS DISTINCT FROM 'managed-compose.interrupting.v1'
   OR snap ?| ARRAY['storage','database','application_database','local_build']
   OR snap->>'image_reference' IS DISTINCT FROM ''
   OR jsonb_typeof(snap->'stack') IS DISTINCT FROM 'object'
   OR (snap->'stack')-'generation'-'hash'<>'{}'::jsonb
   OR coalesce(snap->'stack'->>'generation','') !~ '^[1-9][0-9]{0,18}$'
   OR (snap->'stack'->>'generation')::numeric>9223372036854775807
   OR snap->'stack'->>'generation' IS DISTINCT FROM snap->>'resource_generation'
   OR coalesce(snap->'stack'->>'hash','') !~ '^sha256:[0-9a-f]{64}$'
   OR EXISTS(SELECT 1 FROM hosting_operation_build_artifacts WHERE operation_id=op_key)
   THEN RAISE EXCEPTION 'invalid stack source snapshot' USING ERRCODE='23514'; END IF;
  SELECT definition INTO stack_source FROM hosting_compose_source_versions
   WHERE resource_id=op_row.resource_id AND generation=(snap->'stack'->>'generation')::bigint AND definition_hash=snap->'stack'->>'hash';
  IF NOT FOUND OR stack_source->'container_port' IS DISTINCT FROM snap->'container_port' THEN RAISE EXCEPTION 'stack snapshot source mismatch' USING ERRCODE='23514'; END IF;
  IF snap ? 'runtime_configuration' AND NOT EXISTS(SELECT 1 FROM hosting_runtime_configuration_versions v
   WHERE v.resource_id=op_row.resource_id AND v.project_id=op_row.project_id AND v.generation=(snap->'runtime_configuration'->>'generation')::bigint AND v.body_hash=snap->'runtime_configuration'->>'hash')
   THEN RAISE EXCEPTION 'stack runtime reference mismatch' USING ERRCODE='23514'; END IF;
  IF rel IS NOT NULL AND (jsonb_typeof(rel->'stack') IS DISTINCT FROM 'object' OR (rel->'stack')-'hash'<>'{}'::jsonb
   OR coalesce(rel->'stack'->>'hash','') !~ '^sha256:[0-9a-f]{64}$'
   OR rel->>'canonical_image' IS DISTINCT FROM '' OR rel->>'image_id' IS DISTINCT FROM '' OR rel ? 'local_build'
   OR rel->>'os' IS DISTINCT FROM '' OR rel->>'architecture' IS DISTINCT FROM '' OR coalesce(rel->>'variant','')<>'')
   THEN RAISE EXCEPTION 'invalid stack release union' USING ERRCODE='23514'; END IF;
  IF stack_plan IS NOT NULL AND (stack_plan->>'compiler_version' IS DISTINCT FROM 'managed-compose.compiler.v1'
   OR stack_plan->'snapshot' IS DISTINCT FROM snap OR stack_plan->'release' IS DISTINCT FROM rel
   OR stack_plan->'stack'->'definition' IS DISTINCT FROM stack_source
   OR stack_plan ?| ARRAY['storage','database','application_database']
   OR coalesce((stack_plan->>'retain_previous')::boolean,false)
   OR jsonb_typeof(stack_plan->'stack'->'images') IS DISTINCT FROM 'array'
   OR jsonb_array_length(stack_plan->'stack'->'images') IS DISTINCT FROM jsonb_array_length(stack_source->'services')
   OR EXISTS(SELECT 1 FROM jsonb_array_elements(stack_plan->'stack'->'images') WITH ORDINALITY AS image(body,n)
    WHERE image.body->>'component' IS DISTINCT FROM stack_source->'services'->(image.n::integer-1)->>'name'
    OR image.body->'release' ?| ARRAY['stack','local_build']
    OR image.body->'release'->>'daemon_id' IS DISTINCT FROM rel->>'daemon_id'
    OR coalesce(image.body->'release'->>'canonical_image','') !~ '@sha256:[0-9a-f]{64}$'
    OR coalesce(image.body->'release'->>'image_id','') !~ '^sha256:[0-9a-f]{64}$'))
   THEN RAISE EXCEPTION 'invalid complete stack apply plan' USING ERRCODE='23514'; END IF;
  RETURN NULL;
 END IF;
 IF rel ? 'stack' OR stack_plan ? 'stack' THEN RAISE EXCEPTION 'stack release requires stack snapshot' USING ERRCODE='23514'; END IF;

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

DO $migration$
DECLARE owner_schema text:=current_schema();
BEGIN
 EXECUTE format('ALTER FUNCTION %I.nerocd_hosting_compose_volume_guard() SET search_path TO pg_catalog, %I, pg_temp',owner_schema,owner_schema);
 EXECUTE format('ALTER FUNCTION %I.nerocd_hosting_compose_lease() SET search_path TO pg_catalog, %I, pg_temp',owner_schema,owner_schema);
 EXECUTE format('ALTER FUNCTION %I.nerocd_hosting_build_deployment_consistent() SET search_path TO pg_catalog, %I, pg_temp',owner_schema,owner_schema);
END;$migration$;

CREATE OR REPLACE FUNCTION nerocd_hosting_storage_continuity_consistent() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE op hosting_operations; snap jsonb;
BEGIN
 SELECT * INTO STRICT op FROM hosting_operations WHERE id=NEW.operation_id;
 SELECT body INTO STRICT snap FROM hosting_snapshots WHERE operation_id=NEW.operation_id;
 IF NEW.descriptor->>'operation_id' IS DISTINCT FROM op.id OR NEW.descriptor->>'request_hash' IS DISTINCT FROM op.request_hash OR NEW.descriptor->>'resource_id' IS DISTINCT FROM op.resource_id OR NEW.descriptor->>'binding_id' IS DISTINCT FROM op.binding_id OR NEW.descriptor->>'project_id' IS DISTINCT FROM op.project_id OR NEW.descriptor->>'proxy_id' IS DISTINCT FROM NEW.proxy_id OR NEW.descriptor->'request' IS DISTINCT FROM NEW.accepted_request->'storage_route_continuity' OR snap->'storage_route_continuity'->>'hash' IS DISTINCT FROM NEW.descriptor_hash OR NOT (snap ? 'storage' OR (snap->>'policy_version'='managed-compose.interrupting.v1' AND snap ? 'stack' AND NEW.descriptor->'previous_healthy' ? 'stack')) THEN RAISE EXCEPTION 'storage continuity differs from accepted request'; END IF;
 RETURN NULL;
END;$$;

ALTER TABLE hosting_storage_route_continuities DROP CONSTRAINT hosting_storage_route_continuities_descriptor_check;
ALTER TABLE hosting_storage_route_continuities ADD CONSTRAINT hosting_storage_route_continuities_descriptor_check CHECK(jsonb_typeof(descriptor)='object' AND octet_length(descriptor::text)<=CASE WHEN descriptor->'previous_healthy' ? 'stack' THEN 131072 ELSE 24576 END);
DO $$ BEGIN
 EXECUTE format('ALTER FUNCTION %I.nerocd_hosting_storage_continuity_consistent() SET search_path=%I,pg_catalog,pg_temp',current_schema(),current_schema());
END;$$;
