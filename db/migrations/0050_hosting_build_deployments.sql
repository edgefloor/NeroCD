-- Local artifacts are referenced by exact immutable identity, never a registry tag.
ALTER TABLE hosting_build_artifacts ADD CONSTRAINT hosting_build_artifact_identity UNIQUE(build_id,body_hash);
CREATE TABLE hosting_operation_build_artifacts (
 operation_id text PRIMARY KEY REFERENCES hosting_operations(id) ON DELETE RESTRICT,
 build_id text NOT NULL,
 artifact_hash text NOT NULL CHECK(artifact_hash ~ '^sha256:[a-f0-9]{64}$'),
 CONSTRAINT hosting_operation_artifact_identity FOREIGN KEY(build_id,artifact_hash) REFERENCES hosting_build_artifacts(build_id,body_hash) ON DELETE RESTRICT
);
CREATE TRIGGER hosting_operation_build_artifacts_immutable BEFORE UPDATE OR DELETE ON hosting_operation_build_artifacts FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_execution_immutable();

CREATE FUNCTION nerocd_hosting_build_deployment_consistent() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE op_key text; snap jsonb; rel jsonb; artifact jsonb; artifact_hash text; build_row hosting_builds; op_row hosting_operations; association hosting_operation_build_artifacts;
BEGIN
 op_key:=NEW.operation_id;
 SELECT * INTO op_row FROM hosting_operations WHERE id=op_key;
 SELECT body INTO snap FROM hosting_snapshots WHERE operation_id=op_key;
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
 OR snap->>'image_reference' IS DISTINCT FROM '' OR snap->>'policy_version' IS DISTINCT FROM 'local-build.release-ports.v1'
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
CREATE CONSTRAINT TRIGGER hosting_operation_build_consistency AFTER INSERT ON hosting_operation_build_artifacts DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_build_deployment_consistent();
CREATE CONSTRAINT TRIGGER hosting_snapshot_build_consistency AFTER INSERT ON hosting_snapshots DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_build_deployment_consistent();
CREATE CONSTRAINT TRIGGER hosting_release_build_consistency AFTER INSERT ON hosting_releases DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_build_deployment_consistent();

-- Admission on the lease insert protects the persisted typed relation even when
-- task candidate selection or raw rows omit a capability/target predicate.
CREATE FUNCTION nerocd_local_build_deployment_lease() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE task task_runs; target hosting_task_targets; snap jsonb; local_origin boolean;
BEGIN
 SELECT * INTO task FROM task_runs WHERE id=NEW.run_id;
 SELECT * INTO target FROM hosting_task_targets WHERE run_id=NEW.run_id;
 IF FOUND THEN SELECT body INTO snap FROM hosting_snapshots WHERE operation_id=target.operation_id; END IF;
 local_origin:=coalesce(snap ? 'local_build',false);
 IF local_origin OR task.run_spec->>'type'='local_build_deploy_v1' THEN
  IF NOT local_origin OR task.run_spec->>'type' IS DISTINCT FROM 'local_build_deploy_v1'
  OR task.run_spec->>'hosting_operation_id' IS DISTINCT FROM target.operation_id
  OR task.run_spec->>'hosting_mode' IS DISTINCT FROM target.mode
  OR task.run_spec - 'type' - 'hosting_operation_id' - 'hosting_mode' - 'inputs'<>'{}'::jsonb
  OR coalesce(task.run_spec->'inputs','{}'::jsonb)<>'{}'::jsonb
  OR NEW.runner_id IS DISTINCT FROM target.runner_id
  OR NOT EXISTS(SELECT 1 FROM runners r WHERE r.id=NEW.runner_id AND r.status='active' AND r.token_hash<>'' AND 'local_build_deploy_v1'=ANY(r.capabilities) AND NOT('shell'=ANY(r.capabilities))) THEN
   RAISE EXCEPTION 'local build deployment requires exact typed capability and target' USING ERRCODE='23514';
  END IF;
 END IF;
 RETURN NEW;
END;$$;
CREATE TRIGGER local_build_deployment_lease BEFORE INSERT ON run_leases FOR EACH ROW EXECUTE FUNCTION nerocd_local_build_deployment_lease();
CREATE FUNCTION nerocd_hosting_build_deployment_schema_compatible() RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT nerocd_hosting_git_resource_schema_compatible()
 AND to_regclass(current_schema()||'.hosting_operation_build_artifacts') IS NOT NULL
 AND EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid=to_regclass(current_schema()||'.hosting_operation_build_artifacts') AND conname='hosting_operation_artifact_identity' AND contype='f' AND convalidated)
 AND NOT EXISTS(SELECT 1 FROM (VALUES('hosting_operation_build_artifacts','hosting_operation_build_consistency'),('hosting_snapshots','hosting_snapshot_build_consistency'),('hosting_releases','hosting_release_build_consistency')) t(rel,name) WHERE NOT EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass(current_schema()||'.'||t.rel) AND tgname=t.name AND tgdeferrable AND tginitdeferred AND tgenabled IN('O','A')))
 AND EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass(current_schema()||'.hosting_operation_build_artifacts') AND tgname='hosting_operation_build_artifacts_immutable' AND tgenabled IN('O','A'))
 AND EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass(current_schema()||'.run_leases') AND tgname='local_build_deployment_lease' AND tgenabled IN('O','A'))
 AND NOT EXISTS(SELECT 1 FROM (VALUES('nerocd_hosting_build_deployment_consistent'),('nerocd_local_build_deployment_lease')) f(name) WHERE NOT EXISTS(SELECT 1 FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname=current_schema() AND p.proname=f.name AND p.proconfig @> ARRAY[format('search_path=pg_catalog, %I, pg_temp',current_schema())]));
$$;
DO $migration$
DECLARE owner_schema text:=current_schema();
BEGIN
 EXECUTE format('ALTER FUNCTION %I.nerocd_hosting_build_deployment_consistent() SET search_path TO pg_catalog, %I, pg_temp',owner_schema,owner_schema);
 EXECUTE format('ALTER FUNCTION %I.nerocd_local_build_deployment_lease() SET search_path TO pg_catalog, %I, pg_temp',owner_schema,owner_schema);
 EXECUTE format('ALTER FUNCTION %I.nerocd_hosting_build_deployment_schema_compatible() SET search_path TO %I, pg_catalog, pg_temp',owner_schema,owner_schema);
END;$migration$;
