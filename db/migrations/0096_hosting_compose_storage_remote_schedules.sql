-- Compose schedules use the same immutable remote selection and archive lineage
-- as immediate volume backups. Keep source, actor, revision and target equality.
CREATE OR REPLACE FUNCTION nerocd_storage_backup_schedule_version() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NOT EXISTS(SELECT 1 FROM hosting_storage_backup_schedules s JOIN hosting_storage_backup_schedule_versions v ON v.schedule_id=s.id AND v.revision=s.revision WHERE s.id=NEW.id AND (s.actor_id,s.frequency,s.timezone,s.enabled,s.acknowledge_interruption)=(v.actor_id,v.frequency,v.timezone,v.enabled,v.acknowledge_interruption)) THEN
  RAISE EXCEPTION 'backup schedule version mismatch' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE OR REPLACE FUNCTION nerocd_storage_backup_schedule_occurrence() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE s hosting_storage_backup_schedules; v hosting_storage_backup_schedule_versions;
BEGIN
 IF TG_OP<>'INSERT' THEN RAISE EXCEPTION 'storage backup occurrence immutable' USING ERRCODE='23514'; END IF;
 SELECT * INTO s FROM hosting_storage_backup_schedules WHERE id=NEW.schedule_id;
 SELECT * INTO v FROM hosting_storage_backup_schedule_versions WHERE schedule_id=NEW.schedule_id AND revision=NEW.schedule_revision;
 IF s.id IS NULL OR v.schedule_id IS NULL OR NEW.schedule_revision<>s.revision OR NEW.scheduled_at IS DISTINCT FROM s.last_scheduled_at THEN RAISE EXCEPTION 'storage backup occurrence cursor mismatch' USING ERRCODE='23514'; END IF;
 IF NEW.operation_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM hosting_storage_archive_plans p JOIN hosting_operations o ON o.id=p.operation_id
 WHERE o.id=NEW.operation_id AND p.mode='backup' AND p.source_family=s.source_family AND p.source_resource_id=s.resource_id AND p.source_binding_id=s.binding_id
 AND p.source_storage_id IS NOT DISTINCT FROM s.storage_id AND p.source_volume IS NOT DISTINCT FROM s.source_volume
 AND o.project_id=s.project_id AND o.created_by=v.actor_id AND p.body->'remote_target' IS NOT DISTINCT FROM v.remote_target
 AND p.body->>'destination_id'=s.destination_id
 AND (s.source_family<>'compose' OR v.acknowledge_interruption)) THEN
 RAISE EXCEPTION 'storage backup occurrence operation mismatch' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
DO $$ DECLARE owner_schema text:=current_schema(); routine text; BEGIN
 FOREACH routine IN ARRAY ARRAY['nerocd_storage_backup_schedule_version()','nerocd_storage_backup_schedule_occurrence()'] LOOP EXECUTE format('ALTER FUNCTION %I.%s SET search_path=%I,pg_catalog,pg_temp',owner_schema,routine,owner_schema); END LOOP;
END $$;
