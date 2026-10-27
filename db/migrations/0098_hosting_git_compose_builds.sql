CREATE TABLE hosting_build_compose_results (
 build_id text PRIMARY KEY REFERENCES hosting_builds(id),
 body jsonb NOT NULL CHECK (jsonb_typeof(body)='object'),
 body_hash text NOT NULL CHECK (body_hash ~ '^sha256:[0-9a-f]{64}$'),
 definition_hash text NOT NULL CHECK (definition_hash ~ '^sha256:[0-9a-f]{64}$')
);
CREATE TRIGGER hosting_build_compose_results_immutable BEFORE UPDATE OR DELETE ON hosting_build_compose_results FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_target_immutable();
CREATE FUNCTION nerocd_hosting_build_compose_body() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF jsonb_typeof(NEW.body->'services') IS DISTINCT FROM 'array' OR jsonb_typeof(NEW.body->'definition'->'services') IS DISTINCT FROM 'array' THEN RAISE EXCEPTION 'Compose result requires component arrays' USING ERRCODE='23514'; END IF;
 IF NOT EXISTS(SELECT 1 FROM hosting_builds b JOIN hosting_build_receipts r ON r.build_id=b.id AND r.phase='source' WHERE b.id=NEW.build_id AND b.state='building' AND b.daemon_id<>'' AND b.snapshot->'source'->'settings'->>'strategy'='compose' AND b.snapshot->'operator_policy'->>'git_compose'='true' AND NEW.body->>'source_receipt_hash'=r.body_hash AND NEW.body->>'hash'=NEW.body_hash
 AND NEW.body->'source'->>'public_component'=b.snapshot->'source'->'settings'->>'public_component'
 AND NEW.body->'source'->'container_port'=b.snapshot->'source'->'settings'->'container_port'
 AND NEW.body->'definition'->>'version'='git-compose.v1'
 AND jsonb_typeof(NEW.body->'services')='array' AND jsonb_typeof(NEW.body->'definition'->'services')='array'
 AND jsonb_array_length(NEW.body->'services')=(SELECT count(*) FROM jsonb_array_elements(NEW.body->'definition'->'services') component WHERE component ? 'build')
 AND (SELECT count(DISTINCT component->>'component') FROM jsonb_array_elements(NEW.body->'services') component)=jsonb_array_length(NEW.body->'services')
 AND NOT EXISTS(SELECT 1 FROM jsonb_array_elements(NEW.body->'services') component
  WHERE NOT EXISTS(SELECT 1 FROM jsonb_array_elements(NEW.body->'definition'->'services') owner WHERE owner ? 'build' AND owner->>'name'=component->>'component')
   OR component->'artifact'->>'origin' IS DISTINCT FROM 'local_build'
   OR component->'artifact'->>'build_id' IS DISTINCT FROM ('cmp-'||substr(encode(sha256(convert_to('git-compose.v1','UTF8')||decode('00','hex')||convert_to(b.id,'UTF8')||decode('00','hex')||convert_to(component->>'component','UTF8')),'hex'),1,48))
   OR component->'artifact'->>'source_id' IS DISTINCT FROM b.source_id
   OR component->'artifact'->>'input_id' IS DISTINCT FROM b.snapshot->>'input_id'
   OR component->'artifact'->>'source_receipt_hash' IS DISTINCT FROM r.body_hash
   OR component->'artifact'->>'daemon_id' IS DISTINCT FROM b.daemon_id
   OR component->'artifact'->>'platform' IS DISTINCT FROM b.snapshot->'operator_policy'->>'platform')
 AND NOT EXISTS(SELECT 1 FROM hosting_build_artifacts a WHERE a.build_id=b.id)) THEN RAISE EXCEPTION 'Compose result scope mismatch' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END;$$;
CREATE TRIGGER hosting_build_compose_body BEFORE INSERT ON hosting_build_compose_results FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_build_compose_body();
CREATE OR REPLACE FUNCTION nerocd_hosting_build_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'build history is immutable' USING ERRCODE='23514'; END IF;
 IF (to_jsonb(NEW)-ARRAY['state','revision','run_id','daemon_id','failure_code','updated_at']) IS DISTINCT FROM (to_jsonb(OLD)-ARRAY['state','revision','run_id','daemon_id','failure_code','updated_at']) OR NEW.revision<>OLD.revision+1 OR (OLD.run_id IS NOT NULL AND NEW.run_id IS DISTINCT FROM OLD.run_id) OR (OLD.daemon_id<>'' AND NEW.daemon_id<>OLD.daemon_id) THEN RAISE EXCEPTION 'build identity is immutable' USING ERRCODE='23514'; END IF;
 IF OLD.state IN('succeeded','failed','canceled') OR NOT (NEW.state=OLD.state OR (OLD.state='queued' AND NEW.state IN('sourcing','failed','canceled','cleanup_required')) OR (OLD.state='sourcing' AND NEW.state IN('building','failed','canceled','cleanup_required')) OR (OLD.state='building' AND NEW.state IN('succeeded','failed','canceled','cleanup_required')) OR (OLD.state='cleanup_required' AND NEW.state IN('failed','canceled'))) THEN RAISE EXCEPTION 'invalid build transition' USING ERRCODE='23514'; END IF;
 IF NEW.state='building' AND (NEW.daemon_id='' OR NOT EXISTS(SELECT 1 FROM hosting_build_receipts WHERE build_id=NEW.id AND phase='source')) THEN RAISE EXCEPTION 'build preparation requires source and daemon pin' USING ERRCODE='23514'; END IF;
 IF NEW.state='succeeded' THEN
  IF NEW.snapshot->'source'->'settings'->>'strategy'='compose' THEN
   IF NOT EXISTS(SELECT 1 FROM hosting_build_compose_results WHERE build_id=NEW.id) OR EXISTS(SELECT 1 FROM hosting_build_artifacts WHERE build_id=NEW.id) THEN RAISE EXCEPTION 'Compose success requires exclusive prepared result' USING ERRCODE='23514'; END IF;
  ELSIF NOT EXISTS(SELECT 1 FROM hosting_build_artifacts WHERE build_id=NEW.id) OR EXISTS(SELECT 1 FROM hosting_build_compose_results WHERE build_id=NEW.id) THEN RAISE EXCEPTION 'success requires exclusive artifact' USING ERRCODE='23514'; END IF;
 END IF;
 RETURN NEW;
END;$$;

CREATE OR REPLACE FUNCTION nerocd_hosting_build_deployment_consistent() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE op_key text; snap jsonb; rel jsonb; artifact jsonb; artifact_hash text; build_row hosting_builds; op_row hosting_operations; association hosting_operation_build_artifacts; stack_plan jsonb; stack_source jsonb; compose_result jsonb;
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
  IF snap ? 'git_compose' THEN
   SELECT c.body INTO compose_result FROM hosting_build_compose_results c JOIN hosting_builds b ON b.id=c.build_id
   WHERE c.build_id=snap->'git_compose'->>'build_id' AND c.body_hash=snap->'git_compose'->>'result_hash' AND b.state='succeeded'
    AND b.resource_id=op_row.resource_id AND b.project_id=op_row.project_id
    AND b.snapshot->'source'->>'generation'=snap->'git_compose'->>'source_generation'
    AND b.snapshot->>'resource_generation'=snap->>'resource_generation'
    AND b.snapshot->>'binding_id'=snap->>'binding_id' AND b.snapshot->>'runner_id'=snap->>'runner_id'
    AND b.snapshot->>'destination_id'=snap->>'destination_id' AND b.snapshot->>'server_id'=snap->>'server_id'
    AND c.definition_hash=snap->'stack'->>'hash'
    AND (rel IS NULL OR b.daemon_id=rel->>'daemon_id');
   IF NOT FOUND OR (snap->'git_compose')-'build_id'-'source_generation'-'result_hash'<>'{}'::jsonb THEN RAISE EXCEPTION 'Git stack build reference mismatch' USING ERRCODE='23514'; END IF;
   stack_source:=compose_result->'definition';
  ELSE
   SELECT definition INTO stack_source FROM hosting_compose_source_versions
    WHERE resource_id=op_row.resource_id AND generation=(snap->'stack'->>'generation')::bigint AND definition_hash=snap->'stack'->>'hash';
   IF NOT FOUND THEN RAISE EXCEPTION 'stack source missing' USING ERRCODE='23514'; END IF;
  END IF;
  IF stack_source->'container_port' IS DISTINCT FROM snap->'container_port' THEN RAISE EXCEPTION 'stack snapshot source mismatch' USING ERRCODE='23514'; END IF;
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
    OR image.body->'release' ? 'stack'
    OR (compose_result IS NOT NULL AND NOT (image.body->'release' ? 'local_build') AND (
      stack_source->'services'->(image.n::integer-1) ? 'build'
      OR EXISTS(SELECT 1 FROM jsonb_array_elements(stack_source->'services') owner
       WHERE owner ? 'build' AND coalesce(owner->>'image','')<>'' AND owner->>'image'=stack_source->'services'->(image.n::integer-1)->>'image')))

    OR (image.body->'release' ? 'local_build' AND (compose_result IS NULL OR NOT EXISTS(
      SELECT 1 FROM jsonb_array_elements(compose_result->'services') built
      JOIN jsonb_array_elements(stack_source->'services') owner ON owner->>'name'=built->>'component'
      WHERE built->'artifact'=image.body->'release'->'local_build'
       AND (built->>'component'=image.body->>'component' OR (coalesce(owner->>'image','')<>'' AND owner->>'image'=stack_source->'services'->(image.n::integer-1)->>'image')))))
    OR image.body->'release'->>'daemon_id' IS DISTINCT FROM rel->>'daemon_id'
    OR (NOT (image.body->'release' ? 'local_build') AND coalesce(image.body->'release'->>'canonical_image','') !~ '@sha256:[0-9a-f]{64}$')
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
 EXECUTE format('ALTER FUNCTION %I.nerocd_hosting_build_update() SET search_path TO pg_catalog, %I, pg_temp',owner_schema,owner_schema);
 EXECUTE format('ALTER FUNCTION %I.nerocd_hosting_build_compose_body() SET search_path TO pg_catalog, %I, pg_temp',owner_schema,owner_schema);
 EXECUTE format('ALTER FUNCTION %I.nerocd_hosting_build_deployment_consistent() SET search_path TO pg_catalog, %I, pg_temp',owner_schema,owner_schema);
END;$migration$;

CREATE OR REPLACE FUNCTION nerocd_hosting_build_schema_compatible() RETURNS boolean LANGUAGE sql STABLE AS $$
SELECT to_regclass(current_schema()||'.hosting_builds') IS NOT NULL
 AND to_regclass(current_schema()||'.hosting_build_source_versions') IS NOT NULL
 AND to_regclass(current_schema()||'.hosting_source_grants') IS NOT NULL
 AND to_regclass(current_schema()||'.hosting_build_receipts') IS NOT NULL
 AND to_regclass(current_schema()||'.hosting_build_artifacts') IS NOT NULL
 AND EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass(current_schema()||'.run_leases') AND tgname='hosting_build_lease_admission' AND tgenabled IN('O','A'))
 AND EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass(current_schema()||'.hosting_builds') AND tgname='hosting_build_identity' AND tgenabled IN('O','A'))
 AND EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass(current_schema()||'.hosting_build_artifacts') AND tgname='hosting_build_artifact_body' AND tgenabled IN('O','A'))
 AND to_regclass(current_schema()||'.hosting_build_compose_results') IS NOT NULL
 AND EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass(current_schema()||'.hosting_build_compose_results') AND tgname='hosting_build_compose_body' AND tgenabled IN('O','A'));
$$;
DO $$ BEGIN EXECUTE format('ALTER FUNCTION %I.nerocd_hosting_build_schema_compatible() SET search_path TO %I, pg_catalog, pg_temp',current_schema(),current_schema()); END;$$;
