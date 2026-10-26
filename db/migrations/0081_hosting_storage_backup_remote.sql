CREATE TABLE hosting_storage_backup_transfers (
 id text PRIMARY KEY,
 project_id text NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
 target_id text NOT NULL,
 target_revision bigint NOT NULL,
 destination_id text NOT NULL REFERENCES destinations(id) ON DELETE RESTRICT,
 actor_id text NOT NULL REFERENCES principals(id) ON DELETE RESTRICT,
 request_id text NOT NULL,
 request_hash text NOT NULL CHECK(request_hash ~ '^sha256:[a-f0-9]{64}$'),
 mode text NOT NULL CHECK(mode IN ('validate','upload','download')),
 backup_id text REFERENCES hosting_storage_backups(id) ON DELETE RESTRICT,
 restore_operation_id text REFERENCES hosting_operations(id) ON DELETE RESTRICT,
 predecessor_id text REFERENCES hosting_storage_backup_transfers(id) ON DELETE RESTRICT,
 state text NOT NULL CHECK(state IN ('queued','dispatched','applying','succeeded','failed','uncertain')),
 failure_code text NOT NULL DEFAULT '' CHECK(failure_code IN ('','access_denied','network_denied','remote_unavailable','object_mismatch','archive_unavailable','interrupted','authority_denied')),
 runner_id text NOT NULL REFERENCES runners(id) ON DELETE RESTRICT,
 run_id text UNIQUE REFERENCES task_runs(id) ON DELETE RESTRICT,
 daemon_id text NOT NULL DEFAULT '',
 blob jsonb,
 plan jsonb NOT NULL CHECK(jsonb_typeof(plan)='object'),
 plan_hash text NOT NULL CHECK(plan_hash ~ '^sha256:[a-f0-9]{64}$'),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(project_id,request_id),
 FOREIGN KEY(target_id,target_revision) REFERENCES hosting_database_backup_target_versions(target_id,revision) ON DELETE RESTRICT,
 CHECK((mode='validate' AND backup_id IS NULL AND restore_operation_id IS NULL AND blob IS NULL) OR
       (mode='upload' AND backup_id IS NOT NULL AND restore_operation_id IS NULL AND jsonb_typeof(blob)='object') OR
       (mode='download' AND backup_id IS NOT NULL AND restore_operation_id IS NOT NULL AND jsonb_typeof(blob)='object'))
);
CREATE UNIQUE INDEX hosting_storage_backup_first_upload ON hosting_storage_backup_transfers(backup_id) WHERE mode='upload' AND predecessor_id IS NULL;
CREATE TABLE hosting_storage_backup_transfer_receipts (
 operation_id text NOT NULL REFERENCES hosting_storage_backup_transfers(id) ON DELETE RESTRICT,
 phase text NOT NULL CHECK(phase IN ('intent','complete','failed','uncertain')),
 receipt_id text NOT NULL UNIQUE,
 authority jsonb NOT NULL,
 body jsonb NOT NULL,
 body_hash text NOT NULL CHECK(body_hash ~ '^sha256:[a-f0-9]{64}$'),
 response jsonb NOT NULL,
	 expired_classification boolean NOT NULL DEFAULT false,
 PRIMARY KEY(operation_id,phase)
);
CREATE TABLE hosting_storage_backup_remote_deliveries (
 delivery_id text PRIMARY KEY,
 operation_id text NOT NULL REFERENCES hosting_storage_backup_transfers(id) ON DELETE RESTRICT,
 run_id text NOT NULL REFERENCES task_runs(id) ON DELETE RESTRICT,
 lease_id text NOT NULL REFERENCES run_leases(id) ON DELETE RESTRICT,
 attempt integer NOT NULL CHECK(attempt>0),
 fence text NOT NULL,
 runner_id text NOT NULL REFERENCES runners(id) ON DELETE RESTRICT,
 request_hash text NOT NULL CHECK(request_hash ~ '^sha256:[a-f0-9]{64}$'),
 metadata jsonb NOT NULL,
 use_until timestamptz NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE UNIQUE INDEX hosting_storage_backup_retry_once ON hosting_storage_backup_transfers(predecessor_id) WHERE mode='upload';
CREATE TRIGGER hosting_storage_backup_transfer_identity BEFORE UPDATE OR DELETE ON hosting_storage_backup_transfers FOR EACH ROW EXECUTE FUNCTION nerocd_backup_remote_transfer_identity();
CREATE TRIGGER hosting_storage_backup_transfer_receipts_immutable BEFORE UPDATE OR DELETE ON hosting_storage_backup_transfer_receipts FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_execution_immutable();
CREATE TRIGGER hosting_storage_backup_remote_deliveries_immutable BEFORE UPDATE OR DELETE ON hosting_storage_backup_remote_deliveries FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_execution_immutable();

CREATE UNIQUE INDEX hosting_storage_backup_download_retry_once ON hosting_storage_backup_transfers(predecessor_id,restore_operation_id) WHERE mode='download';

-- Storage authority is derived from an owned immutable storage archive and is
-- never a PostgreSQL backup or permission to move an object across destinations.
CREATE FUNCTION nerocd_storage_backup_remote_admission() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE snapshot jsonb; archive jsonb; original_blob jsonb; original_project text; original_state text; predecessor hosting_storage_backup_transfers; restore jsonb;
BEGIN
 SELECT v.snapshot INTO snapshot FROM hosting_database_backup_target_versions v JOIN hosting_database_backup_targets t ON t.id=v.target_id WHERE v.target_id=NEW.target_id AND v.revision=NEW.target_revision AND t.project_id=NEW.project_id;
 IF snapshot IS NULL OR NEW.plan->'target' IS DISTINCT FROM snapshot OR NEW.plan->>'hash' IS DISTINCT FROM NEW.plan_hash OR NEW.plan->>'actor_id' IS DISTINCT FROM NEW.actor_id OR NEW.plan->>'runner_id' IS DISTINCT FROM NEW.runner_id OR NEW.plan->'operation'->>'id' IS DISTINCT FROM NEW.id OR NEW.plan->'operation'->>'project_id' IS DISTINCT FROM NEW.project_id OR NEW.plan->'operation'->>'target_id' IS DISTINCT FROM NEW.target_id OR NEW.plan->'operation'->>'target_revision' IS DISTINCT FROM NEW.target_revision::text OR NEW.plan->'operation'->>'destination_id' IS DISTINCT FROM NEW.destination_id OR NEW.plan->'operation'->>'mode' IS DISTINCT FROM NEW.mode OR NEW.state<>'queued' OR NEW.run_id IS NOT NULL OR NEW.failure_code<>'' THEN RAISE EXCEPTION 'storage remote admission identity mismatch' USING ERRCODE='23514'; END IF;
 IF NEW.mode<>'validate' THEN
  SELECT p.body,b.body,o.project_id,o.state INTO archive,original_blob,original_project,original_state FROM hosting_storage_backups s JOIN hosting_operations o ON o.id=s.operation_id JOIN hosting_storage_archive_plans p ON p.operation_id=o.id JOIN hosting_storage_archive_blobs b ON b.backup_id=s.id WHERE s.id=NEW.backup_id;
  IF archive IS NULL OR original_state<>'succeeded' OR original_project<>NEW.project_id OR original_blob IS DISTINCT FROM NEW.blob OR NEW.plan->'operation'->'blob' IS DISTINCT FROM NEW.blob OR NEW.plan->'operation'->>'backup_id' IS DISTINCT FROM NEW.backup_id OR archive->>'destination_id' IS DISTINCT FROM NEW.destination_id OR archive->>'runner_id' IS DISTINCT FROM NEW.runner_id OR archive->'helper'->>'daemon_id' IS DISTINCT FROM NEW.daemon_id OR NEW.plan->>'daemon_id' IS DISTINCT FROM NEW.daemon_id OR NEW.plan->>'location_id' IS DISTINCT FROM NEW.backup_id OR NEW.plan->>'object_key' IS DISTINCT FROM ('nerocd/storage/'||NEW.backup_id||'/'||substring(NEW.blob->>'sha256' from 8)||'.tar') THEN RAISE EXCEPTION 'storage remote archive identity mismatch' USING ERRCODE='23514'; END IF;
 END IF;
 IF NEW.predecessor_id IS NOT NULL THEN
  SELECT * INTO predecessor FROM hosting_storage_backup_transfers WHERE id=NEW.predecessor_id;
  IF predecessor.id IS NULL OR predecessor.backup_id IS DISTINCT FROM NEW.backup_id OR predecessor.target_id<>NEW.target_id OR predecessor.target_revision<>NEW.target_revision OR predecessor.blob IS DISTINCT FROM NEW.blob OR predecessor.destination_id<>NEW.destination_id OR (NEW.mode='upload' AND (predecessor.mode<>'upload' OR predecessor.state NOT IN ('failed','uncertain'))) OR (NEW.mode='download' AND NOT ((predecessor.mode='upload' AND predecessor.state='succeeded') OR (predecessor.mode='download' AND predecessor.state IN ('failed','uncertain') AND predecessor.restore_operation_id=NEW.restore_operation_id))) THEN RAISE EXCEPTION 'storage remote predecessor mismatch' USING ERRCODE='23514'; END IF;
 END IF;
 IF NEW.mode='upload' AND NEW.predecessor_id IS NULL AND (archive->'remote_target'->>'target_id' IS DISTINCT FROM NEW.target_id OR archive->'remote_target'->>'revision' IS DISTINCT FROM NEW.target_revision::text) THEN RAISE EXCEPTION 'storage remote selection mismatch' USING ERRCODE='23514'; END IF;
 IF NEW.mode='download' THEN
  SELECT p.body INTO restore FROM hosting_storage_archive_plans p JOIN hosting_operations o ON o.id=p.operation_id WHERE o.id=NEW.restore_operation_id AND o.project_id=NEW.project_id AND p.mode='restore' AND p.backup_id=NEW.backup_id AND p.target_resource_id IS NOT NULL;
  IF restore IS NULL OR restore->>'destination_id' IS DISTINCT FROM NEW.destination_id OR NEW.plan->'operation'->>'restore_operation_id' IS DISTINCT FROM NEW.restore_operation_id THEN RAISE EXCEPTION 'storage remote restore identity mismatch' USING ERRCODE='23514'; END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER hosting_storage_backup_remote_admission BEFORE INSERT ON hosting_storage_backup_transfers FOR EACH ROW EXECUTE FUNCTION nerocd_storage_backup_remote_admission();
