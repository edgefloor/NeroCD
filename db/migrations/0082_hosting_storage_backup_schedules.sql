-- Resource-specific owned-upload storage schedules. This is not a general job
-- scheduler: every admitted occurrence links the existing fenced backup family.
CREATE TABLE hosting_storage_backup_schedules (
 id text PRIMARY KEY,
 project_id text NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
 resource_id text NOT NULL REFERENCES resources(id) ON DELETE RESTRICT,
 binding_id text NOT NULL REFERENCES resource_bindings(id) ON DELETE RESTRICT,
 destination_id text NOT NULL REFERENCES destinations(id) ON DELETE RESTRICT,
 storage_id text NOT NULL REFERENCES hosting_storage(id) ON DELETE RESTRICT,
 source_reference_hash text NOT NULL CHECK(source_reference_hash ~ '^sha256:[a-f0-9]{64}$'),
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
CREATE INDEX hosting_storage_backup_schedule_scan ON hosting_storage_backup_schedules(checked_at,id);
CREATE TABLE hosting_storage_backup_schedule_versions (
 schedule_id text NOT NULL REFERENCES hosting_storage_backup_schedules(id) ON DELETE RESTRICT,
 revision bigint NOT NULL CHECK(revision>0),
 actor_id text NOT NULL REFERENCES principals(id) ON DELETE RESTRICT,
 expected_resource_generation bigint NOT NULL CHECK(expected_resource_generation>0),
 frequency text NOT NULL,
 timezone text NOT NULL,
 enabled boolean NOT NULL,
 remote_target jsonb CHECK(remote_target IS NULL OR jsonb_typeof(remote_target)='object'),
 PRIMARY KEY(schedule_id,revision)
);
CREATE TABLE hosting_storage_backup_schedule_requests (
 resource_id text NOT NULL REFERENCES resources(id) ON DELETE RESTRICT,
 request_id text NOT NULL CHECK(request_id ~ '^[0-9a-f]{32}$'),
 actor_id text NOT NULL REFERENCES principals(id) ON DELETE RESTRICT,
 request_hash text NOT NULL CHECK(request_hash ~ '^sha256:[a-f0-9]{64}$'),
 schedule_id text NOT NULL,
 revision bigint NOT NULL,
 response jsonb NOT NULL CHECK(jsonb_typeof(response)='object'),
 PRIMARY KEY(resource_id,request_id),
 FOREIGN KEY(schedule_id,revision) REFERENCES hosting_storage_backup_schedule_versions(schedule_id,revision) ON DELETE RESTRICT
);
CREATE TABLE hosting_storage_backup_schedule_occurrences (
 id text PRIMARY KEY,
 schedule_id text NOT NULL,
 schedule_revision bigint NOT NULL,
 scheduled_at timestamptz NOT NULL CHECK(date_trunc('minute',scheduled_at)=scheduled_at),
 operation_id text UNIQUE REFERENCES hosting_operations(id) ON DELETE RESTRICT,
 state text NOT NULL CHECK(state IN ('admitted','denied','skipped')),
 reason text NOT NULL CHECK(reason IN ('','authority_denied','source_unavailable','source_busy','placement_changed')),
 enqueued_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(schedule_id,scheduled_at),
 FOREIGN KEY(schedule_id,schedule_revision) REFERENCES hosting_storage_backup_schedule_versions(schedule_id,revision) ON DELETE RESTRICT,
 CHECK((state='admitted' AND operation_id IS NOT NULL AND reason='') OR (state<>'admitted' AND reason<>''))
);
CREATE INDEX hosting_storage_backup_schedule_occurrence_scan ON hosting_storage_backup_schedule_occurrences(enqueued_at,id) WHERE state='admitted';
CREATE TRIGGER hosting_storage_backup_schedule_versions_immutable BEFORE UPDATE OR DELETE ON hosting_storage_backup_schedule_versions FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_execution_immutable();
CREATE TRIGGER hosting_storage_backup_schedule_requests_immutable BEFORE UPDATE OR DELETE ON hosting_storage_backup_schedule_requests FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_execution_immutable();
CREATE FUNCTION nerocd_storage_backup_schedule_identity() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' OR (NEW.id,NEW.project_id,NEW.resource_id,NEW.binding_id,NEW.destination_id,NEW.storage_id,NEW.source_reference_hash,NEW.created_at) IS DISTINCT FROM (OLD.id,OLD.project_id,OLD.resource_id,OLD.binding_id,OLD.destination_id,OLD.storage_id,OLD.source_reference_hash,OLD.created_at) OR
    NEW.revision NOT IN (OLD.revision,OLD.revision+1) OR (OLD.last_scheduled_at IS NOT NULL AND (NEW.last_scheduled_at IS NULL OR NEW.last_scheduled_at<OLD.last_scheduled_at)) OR
    (NEW.revision=OLD.revision AND (NEW.actor_id,NEW.frequency,NEW.timezone,NEW.enabled) IS DISTINCT FROM (OLD.actor_id,OLD.frequency,OLD.timezone,OLD.enabled)) THEN
  RAISE EXCEPTION 'backup schedule identity is immutable' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER hosting_storage_backup_schedule_identity BEFORE UPDATE OR DELETE ON hosting_storage_backup_schedules FOR EACH ROW EXECUTE FUNCTION nerocd_storage_backup_schedule_identity();
CREATE FUNCTION nerocd_storage_backup_schedule_version() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 -- Deferred validation reads the final row, including multiple cursor touches.
 IF NOT EXISTS(SELECT 1 FROM hosting_storage_backup_schedules s JOIN hosting_storage_backup_schedule_versions v ON v.schedule_id=s.id AND v.revision=s.revision WHERE s.id=NEW.id AND (s.actor_id,s.frequency,s.timezone,s.enabled)=(v.actor_id,v.frequency,v.timezone,v.enabled)) THEN
  RAISE EXCEPTION 'backup schedule version mismatch' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE CONSTRAINT TRIGGER hosting_storage_backup_schedule_version AFTER INSERT OR UPDATE ON hosting_storage_backup_schedules DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION nerocd_storage_backup_schedule_version();
CREATE FUNCTION nerocd_storage_backup_schedule_occurrence() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE s hosting_storage_backup_schedules; v hosting_storage_backup_schedule_versions;
BEGIN
 IF TG_OP<>'INSERT' THEN RAISE EXCEPTION 'storage backup occurrence immutable' USING ERRCODE='23514'; END IF;
 SELECT * INTO s FROM hosting_storage_backup_schedules WHERE id=NEW.schedule_id;
 SELECT * INTO v FROM hosting_storage_backup_schedule_versions WHERE schedule_id=NEW.schedule_id AND revision=NEW.schedule_revision;
 IF s.id IS NULL OR v.schedule_id IS NULL OR NEW.schedule_revision<>s.revision OR NEW.scheduled_at IS DISTINCT FROM s.last_scheduled_at THEN RAISE EXCEPTION 'storage backup occurrence cursor mismatch' USING ERRCODE='23514'; END IF;
 IF NEW.operation_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM hosting_storage_archive_plans p JOIN hosting_operations o ON o.id=p.operation_id WHERE o.id=NEW.operation_id AND p.mode='backup' AND p.source_resource_id=s.resource_id AND p.source_binding_id=s.binding_id AND p.source_storage_id=s.storage_id AND o.project_id=s.project_id AND o.created_by=v.actor_id AND p.body->'remote_target' IS NOT DISTINCT FROM v.remote_target) THEN
 RAISE EXCEPTION 'storage backup occurrence operation mismatch' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER hosting_storage_backup_schedule_occurrence BEFORE INSERT OR UPDATE OR DELETE ON hosting_storage_backup_schedule_occurrences FOR EACH ROW EXECUTE FUNCTION nerocd_storage_backup_schedule_occurrence();
DO $$ DECLARE owner_schema text:=current_schema(); routine text; BEGIN
 FOREACH routine IN ARRAY ARRAY['nerocd_storage_backup_schedule_identity()','nerocd_storage_backup_schedule_version()','nerocd_storage_backup_schedule_occurrence()'] LOOP EXECUTE format('ALTER FUNCTION %I.%s SET search_path=%I,pg_catalog,pg_temp',owner_schema,routine,owner_schema); END LOOP;
END $$;


-- Extend 0080 without changing its applied history. Database sources keep their
-- original evidence rules; storage sources use their own accepted receipt family.
ALTER TABLE hosting_notification_source_receipts DROP CONSTRAINT hosting_notification_source_receipts_source_kind_check;
ALTER TABLE hosting_notification_source_receipts ADD CHECK(source_kind IN ('backup','transfer','storage_backup','storage_transfer'));
ALTER TABLE hosting_notification_events DROP CONSTRAINT hosting_notification_events_source_kind_check;
ALTER TABLE hosting_notification_events ADD CHECK(source_kind IN ('backup','transfer','test','storage_backup','storage_transfer'));
CREATE OR REPLACE FUNCTION nerocd_notification_event_source() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP<>'INSERT' THEN RAISE EXCEPTION 'notification event immutable' USING ERRCODE='23514'; END IF;
 IF NEW.source_kind='backup' AND NEW.source_state IS DISTINCT FROM (SELECT state FROM hosting_database_backups WHERE id=NEW.source_id) THEN RAISE EXCEPTION 'notification backup state mismatch' USING ERRCODE='23514'; END IF;
 IF NEW.source_kind='transfer' AND NEW.source_state IS DISTINCT FROM (SELECT state FROM hosting_database_backup_transfers WHERE id=NEW.source_id) THEN RAISE EXCEPTION 'notification upload state mismatch' USING ERRCODE='23514'; END IF;
 IF (NEW.source_kind='test') IS DISTINCT FROM (NEW.source_state='test') THEN RAISE EXCEPTION 'notification test state mismatch' USING ERRCODE='23514'; END IF;
 IF NEW.source_kind='backup' THEN
  IF NEW.event<>'backup_failed' OR NOT EXISTS(SELECT 1 FROM hosting_database_backups b WHERE b.id=NEW.source_id AND b.project_id=NEW.project_id AND b.mode='backup' AND b.state IN ('failed','uncertain') AND b.failure_code=NEW.failure_code AND (EXISTS(SELECT 1 FROM hosting_database_backup_receipts r WHERE r.operation_id=b.id AND r.receipt_id=NEW.source_receipt AND r.phase IN ('failed','uncertain')) OR EXISTS(SELECT 1 FROM hosting_notification_source_receipts r WHERE r.id=NEW.source_receipt AND r.source_kind='backup' AND r.source_id=b.id AND r.source_snapshot=jsonb_build_object('state',b.state,'failure_code',b.failure_code,'plan_hash',b.plan_hash)))) THEN RAISE EXCEPTION 'notification backup source mismatch' USING ERRCODE='23514'; END IF;
 ELSIF NEW.source_kind='transfer' THEN
  IF NEW.event<>'upload_failed' OR NOT EXISTS(SELECT 1 FROM hosting_database_backup_transfers t WHERE t.id=NEW.source_id AND t.project_id=NEW.project_id AND t.mode='upload' AND t.state IN ('failed','uncertain') AND t.failure_code=NEW.failure_code AND (EXISTS(SELECT 1 FROM hosting_database_backup_transfer_receipts r WHERE r.operation_id=t.id AND r.receipt_id=NEW.source_receipt AND r.phase IN ('failed','uncertain')) OR EXISTS(SELECT 1 FROM hosting_notification_source_receipts r WHERE r.id=NEW.source_receipt AND r.source_kind='transfer' AND r.source_id=t.id AND r.source_snapshot=jsonb_build_object('state',t.state,'failure_code',t.failure_code,'plan_hash',t.plan_hash)))) THEN RAISE EXCEPTION 'notification upload source mismatch' USING ERRCODE='23514'; END IF;
 ELSIF NEW.source_kind='storage_backup' THEN
  IF NEW.event<>'backup_failed' OR NOT EXISTS(
   SELECT 1 FROM hosting_operations o JOIN hosting_storage_archive_plans p ON p.operation_id=o.id
   JOIN hosting_storage_archive_receipts receipt ON receipt.operation_id=o.id AND receipt.phase='complete'
   WHERE o.id=NEW.source_id AND o.project_id=NEW.project_id AND p.mode='backup' AND o.state IN ('failed','reconciliation_required')
    AND NEW.source_state=(CASE WHEN o.state='reconciliation_required' THEN 'uncertain' ELSE o.state END)
    AND receipt.receipt_id=NEW.source_receipt AND coalesce(receipt.body->'result'->>'failure_code','')=NEW.failure_code
    AND NEW.local_copy_available=EXISTS(SELECT 1 FROM hosting_storage_archive_blobs b WHERE b.backup_id=p.backup_id)
  ) THEN RAISE EXCEPTION 'notification storage backup source mismatch' USING ERRCODE='23514'; END IF;
 ELSIF NEW.source_kind='storage_transfer' THEN
  IF NEW.event<>'upload_failed' OR NOT EXISTS(
   SELECT 1 FROM hosting_storage_backup_transfers t
   WHERE t.id=NEW.source_id AND t.project_id=NEW.project_id AND t.mode='upload' AND t.state IN ('failed','uncertain') AND t.state=NEW.source_state AND t.failure_code=NEW.failure_code
    AND NEW.local_copy_available=EXISTS(SELECT 1 FROM hosting_storage_archive_blobs b WHERE b.backup_id=t.backup_id)
    AND (EXISTS(SELECT 1 FROM hosting_storage_backup_transfer_receipts receipt WHERE receipt.operation_id=t.id AND receipt.receipt_id=NEW.source_receipt AND receipt.phase IN ('failed','uncertain'))
    OR EXISTS(SELECT 1 FROM hosting_notification_source_receipts receipt WHERE receipt.id=NEW.source_receipt AND receipt.source_kind='storage_transfer' AND receipt.source_id=t.id AND receipt.source_snapshot=jsonb_build_object('state',t.state,'failure_code',t.failure_code,'plan_hash',t.plan_hash)))
  ) THEN RAISE EXCEPTION 'notification storage upload source mismatch' USING ERRCODE='23514'; END IF;
 ELSIF NEW.event<>'test' THEN RAISE EXCEPTION 'notification test source mismatch' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;

DO $$ DECLARE owner_schema text:=current_schema(); BEGIN
 EXECUTE format('ALTER FUNCTION %I.nerocd_notification_event_source() SET search_path=%I,pg_catalog,pg_temp',owner_schema,owner_schema);
END $$;
