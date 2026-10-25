-- Resource-specific hosted PostgreSQL schedules. This is not a general job
-- scheduler: every admitted occurrence links the existing fenced backup family.
CREATE TABLE hosting_database_backup_schedules (
 id text PRIMARY KEY,
 project_id text NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
 resource_id text NOT NULL REFERENCES resources(id) ON DELETE RESTRICT,
 binding_id text NOT NULL REFERENCES resource_bindings(id) ON DELETE RESTRICT,
 destination_id text NOT NULL REFERENCES destinations(id) ON DELETE RESTRICT,
 actor_id text NOT NULL REFERENCES principals(id) ON DELETE RESTRICT,
 revision bigint NOT NULL CHECK(revision>0),
 frequency text NOT NULL CHECK(length(frequency) BETWEEN 1 AND 128),
 timezone text NOT NULL CHECK(length(timezone) BETWEEN 1 AND 128),
 enabled boolean NOT NULL,
 last_scheduled_at timestamptz,
 checked_at timestamptz NOT NULL DEFAULT '1970-01-01 00:00:00+00',
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX hosting_database_backup_schedule_scan ON hosting_database_backup_schedules(checked_at,id);
CREATE TABLE hosting_database_backup_schedule_versions (
 schedule_id text NOT NULL REFERENCES hosting_database_backup_schedules(id) ON DELETE RESTRICT,
 revision bigint NOT NULL CHECK(revision>0),
 actor_id text NOT NULL REFERENCES principals(id) ON DELETE RESTRICT,
 expected_resource_generation bigint NOT NULL CHECK(expected_resource_generation>0),
 frequency text NOT NULL,
 timezone text NOT NULL,
 enabled boolean NOT NULL,
 PRIMARY KEY(schedule_id,revision)
);
CREATE TABLE hosting_database_backup_schedule_requests (
 resource_id text NOT NULL REFERENCES resources(id) ON DELETE RESTRICT,
 request_id text NOT NULL,
 actor_id text NOT NULL REFERENCES principals(id) ON DELETE RESTRICT,
 request_hash text NOT NULL CHECK(request_hash ~ '^sha256:[a-f0-9]{64}$'),
 schedule_id text NOT NULL,
 revision bigint NOT NULL,
 response jsonb NOT NULL CHECK(jsonb_typeof(response)='object'),
 PRIMARY KEY(resource_id,request_id),
 FOREIGN KEY(schedule_id,revision) REFERENCES hosting_database_backup_schedule_versions(schedule_id,revision) ON DELETE RESTRICT
);
CREATE TABLE hosting_database_backup_schedule_occurrences (
 id text PRIMARY KEY,
 schedule_id text NOT NULL,
 schedule_revision bigint NOT NULL,
 scheduled_at timestamptz NOT NULL CHECK(date_trunc('minute',scheduled_at)=scheduled_at),
 operation_id text UNIQUE REFERENCES hosting_database_backups(id) ON DELETE RESTRICT,
 state text NOT NULL CHECK(state IN ('admitted','denied','skipped','missed')),
 reason text NOT NULL CHECK(reason IN ('','authority_denied','source_unavailable','source_busy','placement_changed','newer_occurrence','too_old','queue_unavailable')),
 enqueued_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(schedule_id,scheduled_at),
 FOREIGN KEY(schedule_id,schedule_revision) REFERENCES hosting_database_backup_schedule_versions(schedule_id,revision) ON DELETE RESTRICT,
 CHECK((state='admitted' AND operation_id IS NOT NULL AND reason='') OR (state<>'admitted' AND reason<>''))
);
CREATE INDEX hosting_database_backup_schedule_occurrence_scan ON hosting_database_backup_schedule_occurrences(enqueued_at,id) WHERE state='admitted';
CREATE TRIGGER hosting_database_backup_schedule_versions_immutable BEFORE UPDATE OR DELETE ON hosting_database_backup_schedule_versions FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_execution_immutable();
CREATE TRIGGER hosting_database_backup_schedule_requests_immutable BEFORE UPDATE OR DELETE ON hosting_database_backup_schedule_requests FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_execution_immutable();
CREATE FUNCTION nerocd_database_backup_schedule_identity() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' OR (NEW.id,NEW.project_id,NEW.resource_id,NEW.binding_id,NEW.destination_id,NEW.created_at) IS DISTINCT FROM (OLD.id,OLD.project_id,OLD.resource_id,OLD.binding_id,OLD.destination_id,OLD.created_at) OR
    NEW.revision NOT IN (OLD.revision,OLD.revision+1) OR (OLD.last_scheduled_at IS NOT NULL AND (NEW.last_scheduled_at IS NULL OR NEW.last_scheduled_at<OLD.last_scheduled_at)) OR
    (NEW.revision=OLD.revision AND (NEW.actor_id,NEW.frequency,NEW.timezone,NEW.enabled) IS DISTINCT FROM (OLD.actor_id,OLD.frequency,OLD.timezone,OLD.enabled)) THEN
  RAISE EXCEPTION 'backup schedule identity is immutable' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER hosting_database_backup_schedule_identity BEFORE UPDATE OR DELETE ON hosting_database_backup_schedules FOR EACH ROW EXECUTE FUNCTION nerocd_database_backup_schedule_identity();
CREATE FUNCTION nerocd_database_backup_schedule_version() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 -- Deferred validation reads the final row, including multiple cursor touches.
 IF NOT EXISTS(SELECT 1 FROM hosting_database_backup_schedules s JOIN hosting_database_backup_schedule_versions v ON v.schedule_id=s.id AND v.revision=s.revision WHERE s.id=NEW.id AND (s.actor_id,s.frequency,s.timezone,s.enabled)=(v.actor_id,v.frequency,v.timezone,v.enabled)) THEN
  RAISE EXCEPTION 'backup schedule version mismatch' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE CONSTRAINT TRIGGER hosting_database_backup_schedule_version AFTER INSERT OR UPDATE ON hosting_database_backup_schedules DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION nerocd_database_backup_schedule_version();
CREATE FUNCTION nerocd_database_backup_schedule_occurrence() RETURNS trigger LANGUAGE plpgsql AS $$
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
  IF b.id IS NULL OR b.mode<>'backup' OR b.source_resource_id<>s.resource_id OR b.source_binding_id<>s.binding_id OR b.project_id<>s.project_id OR b.created_by<>v.actor_id OR (NEW.state<>'admitted' AND (b.state<>'failed' OR b.ownership_held)) THEN
   RAISE EXCEPTION 'backup occurrence operation mismatch' USING ERRCODE='23514';
  END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER hosting_database_backup_schedule_occurrence BEFORE INSERT OR UPDATE OR DELETE ON hosting_database_backup_schedule_occurrences FOR EACH ROW EXECUTE FUNCTION nerocd_database_backup_schedule_occurrence();
DO $$ DECLARE owner_schema text:=current_schema(); routine text; BEGIN
 FOREACH routine IN ARRAY ARRAY['nerocd_database_backup_schedule_identity()','nerocd_database_backup_schedule_version()','nerocd_database_backup_schedule_occurrence()'] LOOP EXECUTE format('ALTER FUNCTION %I.%s SET search_path=%I,pg_catalog,pg_temp',owner_schema,routine,owner_schema); END LOOP;
END $$;

-- A controller may retire only a provably never-dispatched schedule occurrence.
-- This is not a runner effect receipt and cannot settle uncertain ownership.
CREATE TABLE hosting_database_backup_schedule_retirements (
 operation_id text PRIMARY KEY REFERENCES hosting_database_backups(id) ON DELETE RESTRICT,
 occurrence_id text NOT NULL UNIQUE REFERENCES hosting_database_backup_schedule_occurrences(id) ON DELETE RESTRICT,
 plan_hash text NOT NULL CHECK(plan_hash ~ '^sha256:[a-f0-9]{64}$'),
 reason text NOT NULL CHECK(reason IN ('authority_denied','source_unavailable','newer_occurrence','too_old')),
 ownership_held boolean NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 CHECK(ownership_held=(reason IN ('authority_denied','source_unavailable')))
);
CREATE FUNCTION nerocd_database_backup_schedule_retirement() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE b hosting_database_backups; o hosting_database_backup_schedule_occurrences; s hosting_database_backup_schedules;
BEGIN
 SELECT * INTO b FROM hosting_database_backups WHERE id=NEW.operation_id FOR UPDATE;
 SELECT * INTO o FROM hosting_database_backup_schedule_occurrences WHERE id=NEW.occurrence_id;
 SELECT * INTO s FROM hosting_database_backup_schedules WHERE id=o.schedule_id;
 IF b.id IS NULL OR o.id IS NULL OR o.operation_id IS DISTINCT FROM b.id OR o.state<>'admitted' OR b.mode<>'backup' OR b.state<>'queued' OR b.run_id IS NOT NULL OR NOT b.ownership_held OR b.plan_hash<>NEW.plan_hash OR EXISTS(SELECT 1 FROM hosting_database_backup_receipts WHERE operation_id=b.id) OR EXISTS(SELECT 1 FROM task_runs r JOIN run_leases l ON l.run_id=r.id WHERE r.run_spec->>'hosting_operation_id'=b.id) OR
 (NEW.reason='newer_occurrence' AND NOT(s.last_scheduled_at>o.scheduled_at)) OR
 (NEW.reason='too_old' AND NOT(o.scheduled_at<clock_timestamp()-interval '24 hours')) THEN
  RAISE EXCEPTION 'backup schedule unstarted retirement proof mismatch' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER hosting_database_backup_schedule_retirement BEFORE INSERT ON hosting_database_backup_schedule_retirements FOR EACH ROW EXECUTE FUNCTION nerocd_database_backup_schedule_retirement();
CREATE TRIGGER hosting_database_backup_schedule_retirement_immutable BEFORE UPDATE OR DELETE ON hosting_database_backup_schedule_retirements FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_execution_immutable();

-- Preserve the complete 0075 identity guard; add only the proved unstarted transition.
CREATE OR REPLACE FUNCTION nerocd_database_backup_identity() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' OR (NEW.id,NEW.project_id,NEW.source_resource_id,NEW.source_binding_id,NEW.source_generation,NEW.backup_id,NEW.mode,NEW.request_id,NEW.request_hash,NEW.created_by,NEW.target_resource_id,NEW.target_binding_id,NEW.deployment_operation_id,NEW.runner_id,NEW.created_at) IS DISTINCT FROM (OLD.id,OLD.project_id,OLD.source_resource_id,OLD.source_binding_id,OLD.source_generation,OLD.backup_id,OLD.mode,OLD.request_id,OLD.request_hash,OLD.created_by,OLD.target_resource_id,OLD.target_binding_id,OLD.deployment_operation_id,OLD.runner_id,OLD.created_at) OR (OLD.run_id IS NOT NULL AND (NEW.run_id,NEW.plan,NEW.plan_hash) IS DISTINCT FROM (OLD.run_id,OLD.plan,OLD.plan_hash)) OR (NOT OLD.ownership_held AND NEW.ownership_held) THEN RAISE EXCEPTION 'database backup identity is immutable' USING ERRCODE='23514'; END IF;
 IF (NEW.plan - ARRAY['target','target_plan','target_storage','hash']) IS DISTINCT FROM (OLD.plan - ARRAY['target','target_plan','target_storage','hash']) THEN RAISE EXCEPTION 'database backup source plan is immutable' USING ERRCODE='23514'; END IF;
 IF NEW.run_id IS NOT NULL AND OLD.run_id IS NULL AND NEW.mode='restore' AND (NEW.plan->'target'->>'operation_id' IS DISTINCT FROM NEW.deployment_operation_id OR NEW.plan->'target'->'snapshot'->>'resource_id' IS DISTINCT FROM NEW.target_resource_id OR NOT EXISTS(SELECT 1 FROM hosting_apply_plans p JOIN hosting_operations o ON o.id=p.operation_id JOIN hosting_binding_state b ON b.binding_id=o.binding_id WHERE o.id=NEW.deployment_operation_id AND o.state='succeeded' AND p.body=NEW.plan->'target_plan' AND b.healthy=NEW.plan->'target')) THEN RAISE EXCEPTION 'database restore dispatch requires exact settled child' USING ERRCODE='23514'; END IF;
 IF (OLD.blob IS NOT NULL AND NEW.blob IS DISTINCT FROM OLD.blob) OR (OLD.state IN ('succeeded','failed','uncertain') AND NEW IS DISTINCT FROM OLD) OR (NEW.state<>OLD.state AND NOT ((OLD.state='queued' AND (NEW.state='dispatched' OR (NEW.state='failed' AND OLD.run_id IS NULL AND NEW.failure_code='interrupted' AND EXISTS(SELECT 1 FROM hosting_database_backup_schedule_retirements r WHERE r.operation_id=OLD.id AND r.plan_hash=OLD.plan_hash AND r.ownership_held=NEW.ownership_held)))) OR (OLD.state='waiting_target' AND NEW.state IN ('dispatched','uncertain')) OR (OLD.state='dispatched' AND NEW.state IN ('applying','failed')) OR (OLD.state='applying' AND NEW.state IN ('succeeded','uncertain')))) THEN RAISE EXCEPTION 'database backup transition or artifact changed' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
DO $$ DECLARE owner_schema text:=current_schema(); routine text; BEGIN
 FOREACH routine IN ARRAY ARRAY['nerocd_database_backup_identity()','nerocd_database_backup_schedule_retirement()','nerocd_database_backup_schedule_identity()','nerocd_database_backup_schedule_version()','nerocd_database_backup_schedule_occurrence()'] LOOP EXECUTE format('ALTER FUNCTION %I.%s SET search_path=%I,pg_catalog,pg_temp',owner_schema,routine,owner_schema); END LOOP;
END $$;