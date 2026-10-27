-- A Compose archive refers to its real materialized stack-volume origin.
ALTER TABLE hosting_storage_archive_plans ALTER COLUMN source_storage_id DROP NOT NULL;
ALTER TABLE hosting_storage_archive_plans ADD COLUMN source_family text NOT NULL DEFAULT 'image' CHECK(source_family IN('image','compose'));
ALTER TABLE hosting_storage_archive_plans ADD COLUMN source_volume text;
ALTER TABLE hosting_storage_archive_plans ADD CONSTRAINT hosting_archive_compose_volume_fk FOREIGN KEY(source_binding_id,source_volume) REFERENCES hosting_compose_volumes(binding_id,name);
DO $$ DECLARE c record; BEGIN
 FOR c IN SELECT conname FROM pg_constraint WHERE conrelid='hosting_storage_archive_plans'::regclass AND contype='c'
 AND (pg_get_constraintdef(oid) LIKE '%compiler_version%' OR pg_get_constraintdef(oid) LIKE '%jsonb_array_length%writers%') LOOP
 EXECUTE format('ALTER TABLE hosting_storage_archive_plans DROP CONSTRAINT %I',c.conname);
 END LOOP;
END $$;
ALTER TABLE hosting_storage_archive_plans ADD CONSTRAINT hosting_archive_source_family CHECK(
 (source_family='image' AND source_storage_id IS NOT NULL AND source_volume IS NULL AND NOT(body ? 'compose_source')
 AND body->>'compiler_version'='owned-upload.local-archive.v1' AND body->>'policy_version'='owned-upload.local-archive.v1'
 AND body->'source_storage'->>'id'=source_storage_id AND body->'source_storage'->>'resource_id'=source_resource_id AND body->'source_storage'->>'binding_id'=source_binding_id
 AND ((target_resource_id IS NULL AND jsonb_array_length(body->'writers') BETWEEN 1 AND 64) OR (target_resource_id IS NOT NULL AND body->'writers' IN ('[]'::jsonb,'null'::jsonb))))
 OR (source_family='compose' AND source_storage_id IS NULL AND source_volume IS NOT NULL AND target_resource_id IS NULL AND mode IN('backup','recover')
 AND body->>'compiler_version'='managed-compose.local-volume-archive.v1' AND body->>'policy_version'='managed-compose.local-volume-archive.v1'
 AND jsonb_typeof(body->'compose_source')='object' AND body->'writers'='[]'::jsonb
 AND NOT(body ? 'remote_target') AND NOT(body ? 'remote_source') AND NOT(body ? 'target') AND NOT(body ? 'blob')) IS TRUE);
ALTER TABLE hosting_storage_archive_plans ADD CONSTRAINT hosting_archive_plan_identity CHECK((body->>'operation_id'=operation_id AND body->>'mode'=mode AND body->>'backup_id'=backup_id AND body->>'hash'=body_hash) IS TRUE);
-- Full stack evidence can exceed the legacy single-container bound.
DO $$ DECLARE c record; BEGIN
 FOR c IN SELECT conname FROM pg_constraint WHERE conrelid='hosting_storage_archive_plans'::regclass AND contype='c' AND pg_get_constraintdef(oid) LIKE '%32768%' LOOP
 EXECUTE format('ALTER TABLE hosting_storage_archive_plans DROP CONSTRAINT %I',c.conname);END LOOP;
END $$;
ALTER TABLE hosting_storage_archive_plans ADD CONSTRAINT hosting_archive_body_bound CHECK(jsonb_typeof(body)='object' AND octet_length(body::text)<=524288);
CREATE OR REPLACE FUNCTION nerocd_storage_archive_consistent() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE p hosting_storage_archive_plans; o hosting_operations; src hosting_storage; dst hosting_storage;
BEGIN
 SELECT * INTO STRICT p FROM hosting_storage_archive_plans WHERE operation_id=NEW.operation_id;
 SELECT * INTO STRICT o FROM hosting_operations WHERE id=p.operation_id;

 IF p.source_family='compose' THEN
  IF p.mode='restore' OR p.target_resource_id IS NOT NULL OR p.source_storage_id IS NOT NULL
   OR o.resource_id IS DISTINCT FROM p.source_resource_id OR o.binding_id IS DISTINCT FROM p.source_binding_id
   OR o.project_id IS DISTINCT FROM p.body->>'project_id'
   OR p.body->'compose_source'->'healthy'->'snapshot'->>'resource_id' IS DISTINCT FROM p.source_resource_id
   OR p.body->'compose_source'->'healthy'->'snapshot'->>'binding_id' IS DISTINCT FROM p.source_binding_id
   OR p.body->'compose_source'->'volume'->>'name' IS DISTINCT FROM p.source_volume
   OR p.body->'compose_source'->'healthy'->'snapshot'->>'destination_id' IS DISTINCT FROM p.body->>'destination_id'
   OR p.body->'compose_source'->'healthy'->'snapshot'->>'runner_id' IS DISTINCT FROM p.body->>'runner_id'
   OR EXISTS(SELECT 1 FROM hosting_apply_plans WHERE operation_id=o.id)
   OR EXISTS(SELECT 1 FROM hosting_releases WHERE operation_id=o.id)
   OR EXISTS(SELECT 1 FROM hosting_receipts WHERE operation_id=o.id)
   OR NOT EXISTS(SELECT 1 FROM hosting_compose_volumes v WHERE v.binding_id=p.source_binding_id AND v.name=p.source_volume
    AND v.state='materialized' AND v.destination_id=p.body->>'destination_id' AND v.daemon_id=p.body->'helper'->>'daemon_id'
    AND v.volume_name=p.body->'compose_source'->'volume'->>'volume_name'
    AND v.creation_operation_id=p.body->'compose_source'->'volume'->>'creation_operation_id'
    AND v.creation_nonce=p.body->'compose_source'->'volume'->>'creation_nonce'
    AND v.created_at=p.body->'compose_source'->'volume'->>'created_at')
   OR NOT EXISTS(SELECT 1 FROM hosting_apply_plans a JOIN hosting_operations d ON d.id=a.operation_id
    WHERE a.operation_id=p.body->'compose_source'->'healthy'->>'operation_id'
    AND a.body=p.body->'compose_source'->'plan' AND a.body_hash=p.body->'compose_source'->'healthy'->>'plan_hash'
    AND d.resource_id=p.source_resource_id AND d.binding_id=p.source_binding_id AND d.project_id=o.project_id AND d.state='succeeded')
  THEN RAISE EXCEPTION 'compose archive source identity mismatch' USING ERRCODE='23514'; END IF;
  IF p.mode='backup' AND NOT EXISTS(SELECT 1 FROM hosting_binding_state b JOIN resources r ON r.id=p.source_resource_id
    WHERE b.binding_id=p.source_binding_id AND b.healthy=p.body->'compose_source'->'healthy' AND b.hold_operation_id=p.operation_id
    AND b.restore_admitted AND r.generation=(p.body->'compose_source'->'healthy'->'snapshot'->>'resource_generation')::bigint)
  THEN RAISE EXCEPTION 'compose archive source is not settled' USING ERRCODE='23514'; END IF;
  IF p.mode='recover' AND NOT EXISTS(SELECT 1 FROM hosting_storage_archive_plans original WHERE original.operation_id=p.recovery_of_operation_id
    AND original.source_family='compose' AND original.source_volume=p.source_volume AND original.body->'compose_source'=p.body->'compose_source')
  THEN RAISE EXCEPTION 'compose archive recovery source mismatch' USING ERRCODE='23514'; END IF;
  RETURN NULL;
 END IF;
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
 RETURN NEW;
END $$;

DO $$ DECLARE owner_schema text:=current_schema(); BEGIN
 EXECUTE format('ALTER FUNCTION %I.nerocd_storage_archive_consistent() SET search_path=%I,pg_catalog,pg_temp',owner_schema,owner_schema);
 EXECUTE format('ALTER FUNCTION %I.nerocd_storage_archive_lease() SET search_path=%I,pg_catalog,pg_temp',owner_schema,owner_schema);
END $$;
