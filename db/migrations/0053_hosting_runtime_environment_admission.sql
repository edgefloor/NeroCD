-- Runtime references change deployment identity without changing build artifacts.
CREATE OR REPLACE FUNCTION nerocd_hosting_build_deployment_consistent() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE op_key text; snap jsonb; rel jsonb; artifact jsonb; artifact_hash text; build_row hosting_builds; op_row hosting_operations; association hosting_operation_build_artifacts;
BEGIN
 op_key:=NEW.operation_id;
 SELECT * INTO op_row FROM hosting_operations WHERE id=op_key;
 SELECT body INTO snap FROM hosting_snapshots WHERE operation_id=op_key;
 -- Preserve historical rows while binding every newly inserted snapshot to
 -- its immutable runtime document. Current generation and secret liveness are
 -- fresh-admission checks, not historical evidence constraints.
 IF snap ? 'runtime_configuration' THEN
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

CREATE FUNCTION nerocd_hosting_runtime_environment_lease() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE snap jsonb; mode text;
BEGIN
 SELECT sn.body,t.mode INTO snap,mode FROM hosting_task_targets t JOIN hosting_snapshots sn ON sn.operation_id=t.operation_id WHERE t.run_id=NEW.run_id;
 IF mode='deploy' AND snap ? 'runtime_configuration' THEN
  PERFORM id FROM runners WHERE id=NEW.runner_id AND status='active' AND token_hash<>''
   AND 'hosting_runtime_env_v1'=ANY(capabilities) AND 'image_release_ports_v1'=ANY(capabilities) FOR SHARE;
  IF NOT FOUND THEN RAISE EXCEPTION 'runtime deployment requires persisted environment and release port capabilities' USING ERRCODE='23514'; END IF;
 END IF;
 RETURN NEW;
END;$$;
CREATE TRIGGER hosting_runtime_environment_lease BEFORE INSERT ON run_leases FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_runtime_environment_lease();

CREATE OR REPLACE FUNCTION nerocd_hosting_build_deployment_schema_compatible() RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT nerocd_hosting_git_resource_schema_compatible()
 AND EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass(current_schema()||'.run_leases') AND tgname='hosting_runtime_environment_lease' AND tgenabled IN('O','A'))
 AND to_regclass(current_schema()||'.hosting_operation_build_artifacts') IS NOT NULL
 AND EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid=to_regclass(current_schema()||'.hosting_operation_build_artifacts') AND conname='hosting_operation_artifact_identity' AND contype='f' AND convalidated)
 AND NOT EXISTS(SELECT 1 FROM (VALUES('hosting_operation_build_artifacts','hosting_operation_build_consistency'),('hosting_snapshots','hosting_snapshot_build_consistency'),('hosting_releases','hosting_release_build_consistency')) t(rel,name) WHERE NOT EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass(current_schema()||'.'||t.rel) AND tgname=t.name AND tgdeferrable AND tginitdeferred AND tgenabled IN('O','A')))
 AND EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass(current_schema()||'.hosting_operation_build_artifacts') AND tgname='hosting_operation_build_artifacts_immutable' AND tgenabled IN('O','A'))
 AND EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass(current_schema()||'.run_leases') AND tgname='local_build_deployment_lease' AND tgenabled IN('O','A'))
 AND NOT EXISTS(SELECT 1 FROM (VALUES('nerocd_hosting_build_deployment_consistent'),('nerocd_local_build_deployment_lease'),('nerocd_hosting_runtime_environment_lease')) f(name) WHERE NOT EXISTS(SELECT 1 FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname=current_schema() AND p.proname=f.name AND p.proconfig @> ARRAY[format('search_path=pg_catalog, %I, pg_temp',current_schema())]));
$$;

CREATE FUNCTION nerocd_hosting_runtime_environment_schema_compatible() RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT nerocd_hosting_build_deployment_schema_compatible()
 AND EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass(current_schema()||'.run_leases') AND tgname='hosting_runtime_environment_lease' AND tgenabled IN('O','A'))
 AND EXISTS(SELECT 1 FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname=current_schema() AND p.proname='nerocd_hosting_runtime_environment_lease' AND p.proconfig @> ARRAY[format('search_path=pg_catalog, %I, pg_temp',current_schema())]);
$$;

DO $migration$
DECLARE owner_schema text:=current_schema();
BEGIN
 EXECUTE format('ALTER FUNCTION %I.nerocd_hosting_build_deployment_consistent() SET search_path TO pg_catalog, %I, pg_temp',owner_schema,owner_schema);
 EXECUTE format('ALTER FUNCTION %I.nerocd_hosting_runtime_environment_lease() SET search_path TO pg_catalog, %I, pg_temp',owner_schema,owner_schema);
 EXECUTE format('ALTER FUNCTION %I.nerocd_hosting_runtime_environment_schema_compatible() SET search_path TO %I, pg_catalog, pg_temp',owner_schema,owner_schema);
 EXECUTE format('ALTER FUNCTION %I.nerocd_hosting_build_deployment_schema_compatible() SET search_path TO %I, pg_catalog, pg_temp',owner_schema,owner_schema);
END;$migration$;
