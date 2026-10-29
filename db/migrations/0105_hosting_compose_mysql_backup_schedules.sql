-- Persist the admitted engine independently of current component availability.
ALTER TABLE hosting_database_backup_schedules ADD COLUMN component_backup_version text NOT NULL DEFAULT '';
ALTER TABLE hosting_database_backup_schedule_versions ADD COLUMN component_backup_version text NOT NULL DEFAULT '';
UPDATE hosting_database_backup_schedules SET component_backup_version='postgresql.compose-logical-local.v1' WHERE component<>'';
-- Only the trusted migration backfills existing immutable revision history.
ALTER TABLE hosting_database_backup_schedule_versions DISABLE TRIGGER hosting_database_backup_schedule_versions_immutable;
UPDATE hosting_database_backup_schedule_versions SET component_backup_version='postgresql.compose-logical-local.v1' WHERE component<>'';
ALTER TABLE hosting_database_backup_schedule_versions ENABLE TRIGGER hosting_database_backup_schedule_versions_immutable;
-- Flush the old deferred row guard before ALTER TABLE, including populated upgrades.
SET CONSTRAINTS hosting_database_backup_schedule_version IMMEDIATE;
ALTER TABLE hosting_database_backup_schedules ADD CONSTRAINT hosting_database_backup_schedule_component_version CHECK ((component='' AND component_backup_version='') OR (component<>'' AND component_backup_version IN ('postgresql.compose-logical-local.v1','mysql.compose-logical-local.v1')));
ALTER TABLE hosting_database_backup_schedule_versions ADD CONSTRAINT hosting_database_backup_schedule_revision_component_version CHECK ((component='' AND component_backup_version='') OR (component<>'' AND component_backup_version IN ('postgresql.compose-logical-local.v1','mysql.compose-logical-local.v1')));

CREATE OR REPLACE FUNCTION nerocd_database_backup_schedule_identity() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' OR (NEW.id,NEW.project_id,NEW.resource_id,NEW.binding_id,NEW.destination_id,NEW.component,NEW.component_backup_version,NEW.created_at) IS DISTINCT FROM (OLD.id,OLD.project_id,OLD.resource_id,OLD.binding_id,OLD.destination_id,OLD.component,OLD.component_backup_version,OLD.created_at) OR
    NEW.revision NOT IN (OLD.revision,OLD.revision+1) OR (OLD.last_scheduled_at IS NOT NULL AND (NEW.last_scheduled_at IS NULL OR NEW.last_scheduled_at<OLD.last_scheduled_at)) OR
    (NEW.revision=OLD.revision AND (NEW.actor_id,NEW.frequency,NEW.timezone,NEW.enabled) IS DISTINCT FROM (OLD.actor_id,OLD.frequency,OLD.timezone,OLD.enabled)) THEN
  RAISE EXCEPTION 'backup schedule identity is immutable' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;

CREATE OR REPLACE FUNCTION nerocd_database_backup_schedule_version() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 -- Deferred validation reads the final row, including multiple cursor touches.
 IF NOT EXISTS(SELECT 1 FROM hosting_database_backup_schedules s JOIN hosting_database_backup_schedule_versions v ON v.schedule_id=s.id AND v.revision=s.revision WHERE s.id=NEW.id AND (s.actor_id,s.frequency,s.timezone,s.enabled,s.component,s.component_backup_version)=(v.actor_id,v.frequency,v.timezone,v.enabled,v.component,v.component_backup_version)) THEN
  RAISE EXCEPTION 'backup schedule version mismatch' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;

CREATE OR REPLACE FUNCTION nerocd_database_backup_schedule_occurrence() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE s hosting_database_backup_schedules; v hosting_database_backup_schedule_versions; b hosting_database_backups;
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'backup occurrence is immutable' USING ERRCODE='23514'; END IF;
 IF TG_OP='UPDATE' THEN
  IF (NEW.id,NEW.schedule_id,NEW.schedule_revision,NEW.scheduled_at,NEW.operation_id,NEW.created_at) IS DISTINCT FROM (OLD.id,OLD.schedule_id,OLD.schedule_revision,OLD.scheduled_at,OLD.operation_id,OLD.created_at) OR OLD.state<>'admitted' THEN
   RAISE EXCEPTION 'backup occurrence identity is immutable' USING ERRCODE='23514';
  END IF;
 END IF;
 SELECT * INTO s FROM hosting_database_backup_schedules WHERE id=NEW.schedule_id;
 SELECT * INTO v FROM hosting_database_backup_schedule_versions WHERE schedule_id=NEW.schedule_id AND revision=NEW.schedule_revision;
 IF s.id IS NULL OR v.schedule_id IS NULL OR (TG_OP='INSERT' AND (NEW.schedule_revision<>s.revision OR NEW.scheduled_at IS DISTINCT FROM s.last_scheduled_at)) THEN RAISE EXCEPTION 'backup occurrence cursor mismatch' USING ERRCODE='23514'; END IF;
 IF NEW.operation_id IS NOT NULL THEN
  SELECT * INTO b FROM hosting_database_backups WHERE id=NEW.operation_id;
  IF b.id IS NULL OR b.mode<>'backup' OR b.source_resource_id<>s.resource_id OR b.source_binding_id<>s.binding_id OR b.project_id<>s.project_id OR b.created_by<>v.actor_id OR coalesce(b.plan->'operation'->>'source_component','')<>v.component OR (v.component<>'' AND (b.plan->'operation'->>'version' IS DISTINCT FROM v.component_backup_version OR b.plan->'component'->>'source' IS DISTINCT FROM v.component OR b.plan->'operation'->>'source_deployed_operation_id' IS DISTINCT FROM b.plan->'source'->>'operation_id')) OR (NEW.state<>'admitted' AND (b.state<>'failed' OR b.ownership_held)) THEN
   RAISE EXCEPTION 'backup occurrence operation mismatch' USING ERRCODE='23514';
  END IF;
 END IF;
 RETURN NEW;
END $$;
DO $$ DECLARE owner_schema text:=current_schema(); routine text; BEGIN
 FOREACH routine IN ARRAY ARRAY['nerocd_database_backup_schedule_identity()','nerocd_database_backup_schedule_version()','nerocd_database_backup_schedule_occurrence()'] LOOP EXECUTE format('ALTER FUNCTION %I.%s SET search_path=pg_catalog,%I,pg_temp',owner_schema,routine,owner_schema); END LOOP;
END $$;

SET CONSTRAINTS hosting_database_backup_schedule_version DEFERRED;
