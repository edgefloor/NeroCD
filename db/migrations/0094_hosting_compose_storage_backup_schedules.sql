-- Schedules pin a real volume origin, while each occurrence resolves the current
-- settled deployment through the existing archive admission transaction.
ALTER TABLE hosting_storage_backup_schedules ALTER COLUMN storage_id DROP NOT NULL;
ALTER TABLE hosting_storage_backup_schedules ADD COLUMN source_family text NOT NULL DEFAULT 'image';
ALTER TABLE hosting_storage_backup_schedules ADD COLUMN source_volume text;
ALTER TABLE hosting_storage_backup_schedules ADD COLUMN acknowledge_interruption boolean NOT NULL DEFAULT false;
ALTER TABLE hosting_storage_backup_schedules ADD CONSTRAINT hosting_storage_schedule_volume_fk FOREIGN KEY(binding_id,source_volume) REFERENCES hosting_compose_volumes(binding_id,name);
ALTER TABLE hosting_storage_backup_schedules ADD CONSTRAINT hosting_storage_schedule_family CHECK(
 (source_family='image' AND storage_id IS NOT NULL AND source_volume IS NULL AND NOT acknowledge_interruption)
 OR (source_family='compose' AND storage_id IS NULL AND source_volume IS NOT NULL AND acknowledge_interruption));
ALTER TABLE hosting_storage_backup_schedule_versions ADD COLUMN acknowledge_interruption boolean NOT NULL DEFAULT false;
CREATE OR REPLACE FUNCTION nerocd_storage_backup_schedule_identity() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' OR (NEW.id,NEW.project_id,NEW.resource_id,NEW.binding_id,NEW.destination_id,NEW.storage_id,NEW.source_family,NEW.source_volume,NEW.source_reference_hash,NEW.acknowledge_interruption,NEW.created_at) IS DISTINCT FROM (OLD.id,OLD.project_id,OLD.resource_id,OLD.binding_id,OLD.destination_id,OLD.storage_id,OLD.source_family,OLD.source_volume,OLD.source_reference_hash,OLD.acknowledge_interruption,OLD.created_at) OR
    NEW.revision NOT IN (OLD.revision,OLD.revision+1) OR (OLD.last_scheduled_at IS NOT NULL AND (NEW.last_scheduled_at IS NULL OR NEW.last_scheduled_at<OLD.last_scheduled_at)) OR
    (NEW.revision=OLD.revision AND (NEW.actor_id,NEW.frequency,NEW.timezone,NEW.enabled) IS DISTINCT FROM (OLD.actor_id,OLD.frequency,OLD.timezone,OLD.enabled)) THEN
  RAISE EXCEPTION 'backup schedule identity is immutable' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE OR REPLACE FUNCTION nerocd_storage_backup_schedule_version() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NOT EXISTS(SELECT 1 FROM hosting_storage_backup_schedules s JOIN hosting_storage_backup_schedule_versions v ON v.schedule_id=s.id AND v.revision=s.revision WHERE s.id=NEW.id AND (s.actor_id,s.frequency,s.timezone,s.enabled,s.acknowledge_interruption)=(v.actor_id,v.frequency,v.timezone,v.enabled,v.acknowledge_interruption) AND (s.source_family<>'compose' OR v.remote_target IS NULL)) THEN
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
 AND (s.source_family<>'compose' OR (v.acknowledge_interruption AND NOT(p.body ? 'remote_target')))) THEN
 RAISE EXCEPTION 'storage backup occurrence operation mismatch' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
DO $$ DECLARE owner_schema text:=current_schema(); routine text; BEGIN
 FOREACH routine IN ARRAY ARRAY['nerocd_storage_backup_schedule_identity()','nerocd_storage_backup_schedule_version()','nerocd_storage_backup_schedule_occurrence()'] LOOP EXECUTE format('ALTER FUNCTION %I.%s SET search_path=%I,pg_catalog,pg_temp',owner_schema,routine,owner_schema); END LOOP;
END $$;
