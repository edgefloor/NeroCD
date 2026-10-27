-- Compose remote selection extends the existing archive and transfer families.
ALTER TABLE hosting_storage_archive_plans DROP CONSTRAINT hosting_archive_source_family;
ALTER TABLE hosting_storage_archive_plans ADD CONSTRAINT hosting_archive_source_family CHECK(
 (source_family='image' AND source_storage_id IS NOT NULL AND source_volume IS NULL AND NOT(body ? 'compose_source')
 AND body->>'compiler_version'='owned-upload.local-archive.v1' AND body->>'policy_version'='owned-upload.local-archive.v1'
 AND body->'source_storage'->>'id'=source_storage_id AND body->'source_storage'->>'resource_id'=source_resource_id AND body->'source_storage'->>'binding_id'=source_binding_id
 AND ((target_resource_id IS NULL AND jsonb_array_length(body->'writers') BETWEEN 1 AND 64) OR (target_resource_id IS NOT NULL AND body->'writers' IN ('[]'::jsonb,'null'::jsonb))))
 OR (source_family='compose' AND source_storage_id IS NULL AND source_volume IS NOT NULL AND target_resource_id IS NULL AND mode IN('backup','recover')
 AND body->>'compiler_version'='managed-compose.local-volume-archive.v1' AND body->>'policy_version'='managed-compose.local-volume-archive.v1'
 AND jsonb_typeof(body->'compose_source')='object' AND body->'writers'='[]'::jsonb
 AND NOT(body ? 'remote_target') AND NOT(body ? 'remote_source') AND NOT(body ? 'target') AND NOT(body ? 'blob')) IS TRUE
 OR (source_family='compose' AND source_storage_id IS NULL AND source_volume IS NOT NULL AND target_resource_id IS NULL AND mode IN('backup','recover')
 AND body->>'compiler_version'='managed-compose.remote-volume-archive.v1' AND body->>'policy_version'='managed-compose.remote-volume-archive.v1'
 AND jsonb_typeof(body->'compose_source')='object' AND body->'writers'='[]'::jsonb
 AND NOT(body ? 'remote_source') AND NOT(body ? 'target') AND NOT(body ? 'blob')
 AND ((mode='backup' AND jsonb_typeof(body->'remote_target')='object'
  AND body->'remote_target'->>'target_id' ~ '^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$'
  AND jsonb_typeof(body->'remote_target'->'revision')='number' AND body->'remote_target'->>'revision' ~ '^[1-9][0-9]*$')
 OR (mode='recover' AND NOT(body ? 'remote_target')))) IS TRUE);
CREATE OR REPLACE FUNCTION nerocd_storage_archive_lease() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE t hosting_storage_archive_task_targets; spec jsonb; p hosting_storage_archive_plans;
BEGIN
 SELECT * INTO t FROM hosting_storage_archive_task_targets WHERE run_id=NEW.run_id;
 IF NOT FOUND THEN RETURN NEW; END IF;
 SELECT run_spec INTO STRICT spec FROM task_runs WHERE id=t.run_id;
 SELECT * INTO STRICT p FROM hosting_storage_archive_plans WHERE operation_id=t.operation_id;
 IF NEW.runner_id<>t.runner_id OR t.mode<>p.mode OR spec IS DISTINCT FROM jsonb_build_object('type','storage_archive','hosting_operation_id',t.operation_id,'hosting_mode',t.mode,'inputs','{}'::jsonb) THEN RAISE EXCEPTION 'archive lease target mismatch' USING ERRCODE='23514'; END IF;
 PERFORM id FROM runners WHERE id=t.runner_id AND status='active' AND token_hash<>'' AND 'storage_archive'=ANY(capabilities) AND 'hosting_storage_archive_v1'=ANY(capabilities) AND 'hosting_owned_storage_v1'=ANY(capabilities) AND NOT('shell'=ANY(capabilities)) FOR SHARE;
 IF NOT FOUND THEN RAISE EXCEPTION 'archive runner not qualified' USING ERRCODE='23514'; END IF;
 IF p.source_family='compose' THEN
  PERFORM id FROM runners WHERE id=t.runner_id AND status='active' AND token_hash<>''
   AND capabilities @> ARRAY['hosting_compose_storage_archive_v1','hosting_managed_compose_v1']::text[] FOR SHARE;
  IF NOT FOUND THEN RAISE EXCEPTION 'compose archive runner not qualified' USING ERRCODE='23514'; END IF;
 END IF;
 IF p.source_family='compose' AND p.body->>'compiler_version'='managed-compose.remote-volume-archive.v1' THEN
  PERFORM id FROM runners WHERE id=t.runner_id AND 'hosting_compose_storage_remote_v1'=ANY(capabilities)
   AND (p.mode='recover' OR capabilities @> ARRAY['hosting_storage_remote_backup_v1','storage_backup_transfer']::text[]) FOR SHARE;
  IF NOT FOUND THEN RAISE EXCEPTION 'compose remote archive runner not qualified' USING ERRCODE='23514'; END IF;
 END IF;
 RETURN NEW;
END $$;


-- Recovery clears target selection but cannot change the accepted plan family.
CREATE FUNCTION nerocd_compose_remote_archive_lineage() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.source_family='compose' AND NEW.mode='recover' AND NOT EXISTS(
  SELECT 1 FROM hosting_storage_archive_plans original WHERE original.operation_id=NEW.recovery_of_operation_id
   AND original.source_family='compose' AND original.mode='backup'
   AND original.body->>'compiler_version'=NEW.body->>'compiler_version'
   AND original.body->>'policy_version'=NEW.body->>'policy_version'
   AND original.body_hash=NEW.body->'recovery_evidence'->>'original_plan_hash'
 ) THEN RAISE EXCEPTION 'compose archive recovery lineage mismatch' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER hosting_compose_remote_archive_lineage BEFORE INSERT ON hosting_storage_archive_plans FOR EACH ROW EXECUTE FUNCTION nerocd_compose_remote_archive_lineage();
DO $$ DECLARE owner_schema text:=current_schema(); routine text; BEGIN
 FOREACH routine IN ARRAY ARRAY['nerocd_storage_archive_lease()','nerocd_compose_remote_archive_lineage()'] LOOP EXECUTE format('ALTER FUNCTION %I.%s SET search_path=%I,pg_catalog,pg_temp',owner_schema,routine,owner_schema); END LOOP;
END $$;
