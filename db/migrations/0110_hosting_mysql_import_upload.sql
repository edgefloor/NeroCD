-- Local uploads use the existing private inspection and import authority.
DO $$
DECLARE constraint_name text; matches integer;
BEGIN
 SELECT count(*),min(conname) INTO matches,constraint_name FROM pg_constraint
 WHERE conrelid='hosting_database_import_stages'::regclass AND contype='c'
 AND pg_get_constraintdef(oid) LIKE '%mysql.compose-external-s3-stage.v1%';
 IF matches<>1 THEN RAISE EXCEPTION 'external stage version constraint mismatch'; END IF;
 EXECUTE format('ALTER TABLE hosting_database_import_stages DROP CONSTRAINT %I',constraint_name);
END $$;
ALTER TABLE hosting_database_import_stages ADD CONSTRAINT hosting_database_import_stage_version
 CHECK(body->>'version' IN ('mysql.compose-external-s3-stage.v1','mysql.compose-local-upload-stage.v1'));

CREATE OR REPLACE FUNCTION nerocd_external_import_lease() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE spec jsonb; expected_runner text; expected_mode text; expected_id text; caps text[]; stage_version text;
BEGIN
 SELECT run_spec INTO spec FROM task_runs WHERE id=NEW.run_id;
 IF spec->>'type' NOT IN ('database_import_stage','database_import') THEN RETURN NEW; END IF;
 expected_mode:=CASE WHEN spec->>'type'='database_import_stage' THEN 'stage' ELSE 'import' END;
 IF expected_mode='stage' THEN
  SELECT id,runner_id,body->>'version' INTO expected_id,expected_runner,stage_version FROM hosting_database_import_stages WHERE run_id=NEW.run_id AND body->>'state'='queued';
 ELSE
  SELECT id,runner_id,plan->'stage_plan'->'stage'->>'version' INTO expected_id,expected_runner,stage_version FROM hosting_database_imports WHERE run_id=NEW.run_id AND ownership_held AND body->>'state'='queued';
 END IF;
 SELECT capabilities INTO caps FROM runners WHERE id=NEW.runner_id AND status='active' AND token_hash<>'' FOR SHARE;
 IF expected_id IS NULL OR expected_runner IS DISTINCT FROM NEW.runner_id OR caps IS NULL OR 'shell'=ANY(caps) OR NOT(caps @> ARRAY[spec->>'type','hosting_managed_compose_v1',CASE WHEN expected_mode='stage' THEN CASE WHEN stage_version='mysql.compose-local-upload-stage.v1' THEN 'hosting_compose_mysql_import_upload_v1' ELSE 'hosting_compose_mysql_import_stage_v1' END ELSE 'hosting_compose_mysql_import_v1' END]::text[]) OR (stage_version='mysql.compose-local-upload-stage.v1' AND NOT('hosting_compose_mysql_import_upload_v1'=ANY(caps))) OR spec<>jsonb_build_object('type',spec->>'type','hosting_operation_id',expected_id,'hosting_mode',expected_mode,'inputs','{}'::jsonb) OR EXISTS(SELECT 1 FROM run_leases WHERE run_id=NEW.run_id) OR NOT EXISTS(SELECT 1 FROM runner_enrollments e JOIN runners r ON r.id=e.runner_id WHERE e.runner_id=NEW.runner_id AND e.used_at IS NOT NULL AND e.revoked_at IS NULL AND e.credential_hash=r.token_hash) THEN
  RAISE EXCEPTION 'external database import lease mismatch' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
DO $$ BEGIN EXECUTE format('ALTER FUNCTION nerocd_external_import_lease() SET search_path TO pg_catalog,%I',current_schema()); END $$;
