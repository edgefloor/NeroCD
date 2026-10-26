-- Copy availability is separate from immutable logical backup history.
CREATE TABLE hosting_storage_backup_retention_policies (
 resource_id text PRIMARY KEY REFERENCES resources(id) ON DELETE RESTRICT,
 project_id text NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
 actor_id text NOT NULL REFERENCES principals(id) ON DELETE RESTRICT,
 revision bigint NOT NULL CHECK(revision>0),
 body jsonb NOT NULL CHECK(jsonb_typeof(body)='object')
);
CREATE TABLE hosting_storage_backup_retention_requests (
 resource_id text NOT NULL REFERENCES resources(id) ON DELETE RESTRICT,
 request_id text NOT NULL, kind text NOT NULL CHECK(kind IN ('policy','preview','execution')),
 actor_id text NOT NULL REFERENCES principals(id) ON DELETE RESTRICT,
 request_hash text NOT NULL, response jsonb NOT NULL,
 PRIMARY KEY(resource_id,kind,request_id)
);
CREATE TRIGGER hosting_storage_backup_retention_requests_immutable BEFORE UPDATE OR DELETE ON hosting_storage_backup_retention_requests FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_execution_immutable();
CREATE TABLE hosting_storage_backup_copies (
 id text PRIMARY KEY,
 backup_id text NOT NULL REFERENCES hosting_storage_backups(id) ON DELETE RESTRICT,
 transfer_id text REFERENCES hosting_storage_backup_transfers(id) ON DELETE RESTRICT,
 resource_id text NOT NULL REFERENCES resources(id) ON DELETE RESTRICT,
 runner_id text NOT NULL REFERENCES runners(id) ON DELETE RESTRICT,
 location text NOT NULL CHECK(location IN ('local','remote')),
 identity_hash text NOT NULL,
 location_hash text NOT NULL,
 body jsonb NOT NULL CHECK(jsonb_typeof(body)='object'),
 availability text NOT NULL CHECK(availability IN ('available','reserved','deleted','uncertain','unproven')),
 superseded_by_copy_id text REFERENCES hosting_storage_backup_copies(id) DEFERRABLE INITIALLY DEFERRED,
 created_at timestamptz NOT NULL
);
CREATE UNIQUE INDEX hosting_storage_backup_copy_current_location ON hosting_storage_backup_copies(backup_id,location,location_hash) WHERE availability<>'deleted' AND superseded_by_copy_id IS NULL;
CREATE INDEX hosting_storage_backup_copy_identity ON hosting_storage_backup_copies(identity_hash);
CREATE INDEX hosting_storage_backup_copy_location ON hosting_storage_backup_copies(location_hash);
CREATE TABLE hosting_storage_backup_retention_previews (
 id text PRIMARY KEY,resource_id text NOT NULL REFERENCES resources(id) ON DELETE RESTRICT,
 actor_id text NOT NULL REFERENCES principals(id) ON DELETE RESTRICT,
 policy_revision bigint NOT NULL,body jsonb NOT NULL,copies jsonb NOT NULL,
 plan_hash text NOT NULL CHECK(plan_hash ~ '^sha256:[a-f0-9]{64}$')
);
CREATE TRIGGER hosting_storage_backup_retention_previews_immutable BEFORE UPDATE OR DELETE ON hosting_storage_backup_retention_previews FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_execution_immutable();
CREATE TABLE hosting_storage_backup_retention_executions (
 id text PRIMARY KEY, resource_id text NOT NULL REFERENCES resources(id) ON DELETE RESTRICT,
 actor_id text NOT NULL REFERENCES principals(id) ON DELETE RESTRICT,
 runner_id text NOT NULL REFERENCES runners(id) ON DELETE RESTRICT,
 preview_id text NOT NULL UNIQUE REFERENCES hosting_storage_backup_retention_previews(id) ON DELETE RESTRICT,
 run_id text UNIQUE REFERENCES task_runs(id) ON DELETE RESTRICT,
 plan jsonb NOT NULL,plan_hash text NOT NULL CHECK(plan_hash ~ '^sha256:[a-f0-9]{64}$'),
 body jsonb NOT NULL,reservation_held boolean NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE hosting_storage_backup_retention_items (
 execution_id text NOT NULL REFERENCES hosting_storage_backup_retention_executions(id) ON DELETE RESTRICT,
 copy_id text NOT NULL REFERENCES hosting_storage_backup_copies(id) ON DELETE RESTRICT,
 identity_hash text NOT NULL, location_hash text NOT NULL,
 state text NOT NULL CHECK(state IN ('pending','intent','deleted','absent','failed','uncertain')),
 reservation_held boolean NOT NULL,
 PRIMARY KEY(execution_id,copy_id)
);
CREATE UNIQUE INDEX hosting_storage_backup_retention_reserved_identity ON hosting_storage_backup_retention_items(identity_hash) WHERE reservation_held;
CREATE UNIQUE INDEX hosting_storage_backup_retention_reserved_location ON hosting_storage_backup_retention_items(location_hash) WHERE reservation_held;
CREATE TABLE hosting_storage_backup_retention_receipts (
 execution_id text NOT NULL REFERENCES hosting_storage_backup_retention_executions(id) ON DELETE RESTRICT,
 copy_id text NOT NULL,phase text NOT NULL,attempt integer NOT NULL CHECK(attempt>0),receipt_id text NOT NULL UNIQUE,
 authority jsonb NOT NULL,body jsonb NOT NULL,body_hash text NOT NULL,response jsonb NOT NULL,
 PRIMARY KEY(execution_id,copy_id,phase,attempt),
 FOREIGN KEY(execution_id,copy_id) REFERENCES hosting_storage_backup_retention_items(execution_id,copy_id) ON DELETE RESTRICT
);
CREATE TRIGGER hosting_storage_backup_retention_receipts_immutable BEFORE UPDATE OR DELETE ON hosting_storage_backup_retention_receipts FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_execution_immutable();
CREATE TABLE hosting_storage_backup_retention_deliveries (
 delivery_id text PRIMARY KEY,
 execution_id text NOT NULL REFERENCES hosting_storage_backup_retention_executions(id) ON DELETE RESTRICT,
 copy_id text NOT NULL,
 metadata jsonb NOT NULL,use_until timestamptz NOT NULL,
 FOREIGN KEY(execution_id,copy_id) REFERENCES hosting_storage_backup_retention_items(execution_id,copy_id) ON DELETE RESTRICT
);
CREATE TRIGGER hosting_storage_backup_retention_deliveries_immutable BEFORE UPDATE OR DELETE ON hosting_storage_backup_retention_deliveries FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_execution_immutable();

CREATE TABLE hosting_storage_backup_retention_publications (
 id text PRIMARY KEY,resource_id text NOT NULL REFERENCES resources(id) ON DELETE RESTRICT,
 actor_id text NOT NULL REFERENCES principals(id) ON DELETE RESTRICT,
 policy_revision bigint NOT NULL,
 state text NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','admitted','skipped','denied')),
 reason text NOT NULL DEFAULT '' CHECK(reason IN ('','no_eligible_copy','authority_denied','scope_changed')),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
-- Immutable copy/selection identities are never rewritten to select a retry's
-- next batch, redirect an endpoint, or resurrect a deleted physical copy.
CREATE FUNCTION nerocd_storage_retention_identity() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'retention history is immutable' USING ERRCODE='23514'; END IF;
 IF TG_TABLE_NAME='hosting_storage_backup_copies' THEN
  IF (NEW.id,NEW.backup_id,NEW.transfer_id,NEW.resource_id,NEW.runner_id,NEW.location,NEW.identity_hash,NEW.location_hash,NEW.body,NEW.created_at) IS DISTINCT FROM (OLD.id,OLD.backup_id,OLD.transfer_id,OLD.resource_id,OLD.runner_id,OLD.location,OLD.identity_hash,OLD.location_hash,OLD.body,OLD.created_at) OR (OLD.availability='deleted' AND NEW.availability<>'deleted') THEN RAISE EXCEPTION 'copy identity is immutable' USING ERRCODE='23514'; END IF;
 ELSIF TG_TABLE_NAME='hosting_storage_backup_retention_executions' THEN
  IF (NEW.id,NEW.resource_id,NEW.actor_id,NEW.runner_id,NEW.preview_id,NEW.plan,NEW.plan_hash,NEW.created_at) IS DISTINCT FROM (OLD.id,OLD.resource_id,OLD.actor_id,OLD.runner_id,OLD.preview_id,OLD.plan,OLD.plan_hash,OLD.created_at) OR (OLD.run_id IS NOT NULL AND NEW.run_id IS DISTINCT FROM OLD.run_id) OR (NOT OLD.reservation_held AND NEW.reservation_held) THEN RAISE EXCEPTION 'retention execution identity is immutable' USING ERRCODE='23514'; END IF;
 ELSE
  IF (NEW.execution_id,NEW.copy_id,NEW.identity_hash,NEW.location_hash) IS DISTINCT FROM (OLD.execution_id,OLD.copy_id,OLD.identity_hash,OLD.location_hash) OR (NOT OLD.reservation_held AND NEW.reservation_held) THEN RAISE EXCEPTION 'retention item identity is immutable' USING ERRCODE='23514'; END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER hosting_storage_backup_copy_identity BEFORE UPDATE OR DELETE ON hosting_storage_backup_copies FOR EACH ROW EXECUTE FUNCTION nerocd_storage_retention_identity();
CREATE TRIGGER hosting_storage_backup_retention_execution_identity BEFORE UPDATE OR DELETE ON hosting_storage_backup_retention_executions FOR EACH ROW EXECUTE FUNCTION nerocd_storage_retention_identity();
CREATE TRIGGER hosting_storage_backup_retention_item_identity BEFORE UPDATE OR DELETE ON hosting_storage_backup_retention_items FOR EACH ROW EXECUTE FUNCTION nerocd_storage_retention_identity();
CREATE FUNCTION nerocd_storage_retention_lease() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE e hosting_storage_backup_retention_executions; r runners; spec jsonb;
BEGIN
 SELECT run_spec INTO spec FROM task_runs WHERE id=NEW.run_id;
 IF spec->>'type' IS DISTINCT FROM 'storage_backup_retention' THEN RETURN NEW; END IF;
 SELECT * INTO e FROM hosting_storage_backup_retention_executions WHERE run_id=NEW.run_id;
 SELECT * INTO r FROM runners WHERE id=NEW.runner_id;
 IF e.id IS NULL OR e.runner_id<>r.id OR NOT e.reservation_held OR e.body->>'state'<>'dispatched' OR r.status<>'active' OR r.token_hash='' OR NOT('storage_backup_retention'=ANY(r.capabilities)) OR NOT('hosting_storage_backup_retention_v1'=ANY(r.capabilities)) OR 'shell'=ANY(r.capabilities) OR spec<>jsonb_build_object('type','storage_backup_retention','hosting_operation_id',e.id,'hosting_mode','retention','inputs','{}'::jsonb) OR (EXISTS(SELECT 1 FROM run_leases WHERE run_id=NEW.run_id) AND (NOT EXISTS(SELECT 1 FROM hosting_storage_backup_retention_resumes a WHERE a.execution_id=e.id AND a.next_attempt=NEW.attempt AND a.plan_hash=e.plan_hash) OR EXISTS(SELECT 1 FROM run_leases prior_lease WHERE prior_lease.run_id=NEW.run_id AND prior_lease.status='active' AND prior_lease.expires_at>clock_timestamp()))) THEN RAISE EXCEPTION 'retention lease identity mismatch' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER hosting_storage_backup_retention_lease BEFORE INSERT ON run_leases FOR EACH ROW EXECUTE FUNCTION nerocd_storage_retention_lease();
CREATE FUNCTION nerocd_storage_retention_receipt() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE e hosting_storage_backup_retention_executions; l run_leases; i hosting_storage_backup_retention_items;
BEGIN
 SELECT * INTO e FROM hosting_storage_backup_retention_executions WHERE id=NEW.execution_id;
 SELECT * INTO l FROM run_leases WHERE id=NEW.authority->>'lease_id';
 SELECT * INTO i FROM hosting_storage_backup_retention_items WHERE execution_id=NEW.execution_id AND copy_id=NEW.copy_id;
 IF e.id IS NULL OR l.id IS NULL OR i.copy_id IS NULL OR e.run_id IS DISTINCT FROM l.run_id OR e.runner_id IS DISTINCT FROM l.runner_id OR NEW.authority->>'operation_id' IS DISTINCT FROM e.id OR NEW.authority->>'run_id' IS DISTINCT FROM e.run_id OR NEW.authority->>'attempt' IS DISTINCT FROM l.attempt::text OR NEW.authority->>'fence' IS DISTINCT FROM l.fence OR l.expires_at<=clock_timestamp() OR NEW.response IS DISTINCT FROM e.body OR (NEW.phase NOT LIKE 'probe_%' AND NEW.phase IS DISTINCT FROM i.state) THEN RAISE EXCEPTION 'retention receipt authority mismatch' USING ERRCODE='23514'; END IF;
 IF NEW.attempt<>l.attempt OR NEW.body->>'receipt_id' IS DISTINCT FROM NEW.receipt_id OR NEW.body->>'copy_id' IS DISTINCT FROM NEW.copy_id OR NEW.body->>'plan_hash' IS DISTINCT FROM e.plan_hash OR NEW.body - ARRAY['receipt_id','copy_id','plan_hash','phase','failure_code','probe_only','probe_key'] IS DISTINCT FROM NEW.authority OR NEW.phase IS DISTINCT FROM (CASE WHEN NEW.body->>'probe_only'='true' THEN 'probe_'||(NEW.body->>'phase') ELSE NEW.body->>'phase' END) THEN RAISE EXCEPTION 'retention receipt body mismatch' USING ERRCODE='23514'; END IF;
 IF (e.body->>'state' IN ('dispatched','applying') AND l.status<>'active') OR (e.body->>'state'='succeeded' AND l.status<>'succeeded') OR (e.body->>'state' IN ('failed','uncertain') AND l.status<>'failed') THEN RAISE EXCEPTION 'retention receipt settlement mismatch' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER hosting_storage_backup_retention_receipt BEFORE INSERT ON hosting_storage_backup_retention_receipts FOR EACH ROW EXECUTE FUNCTION nerocd_storage_retention_receipt();
CREATE TABLE hosting_storage_backup_retention_resumes (
 execution_id text NOT NULL REFERENCES hosting_storage_backup_retention_executions(id) ON DELETE RESTRICT,
 next_attempt integer NOT NULL CHECK(next_attempt>1),
 request_id text NOT NULL,actor_id text NOT NULL REFERENCES principals(id) ON DELETE RESTRICT,
 plan_hash text NOT NULL,created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(execution_id,next_attempt),UNIQUE(execution_id,request_id)
);
CREATE TRIGGER hosting_storage_backup_retention_resumes_immutable BEFORE UPDATE OR DELETE ON hosting_storage_backup_retention_resumes FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_execution_immutable();


-- Shared serial admission covers every storage publication, transfer and restore.
CREATE FUNCTION nerocd_storage_copy_admission() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 PERFORM pg_advisory_xact_lock(hashtextextended('nerocd-storage-copy-admission',0));
 IF EXISTS(SELECT 1 FROM hosting_storage_backup_retention_executions WHERE reservation_held) THEN
 RAISE EXCEPTION 'storage copy pool reserved' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER hosting_storage_copy_archive BEFORE INSERT ON hosting_storage_archive_plans FOR EACH ROW EXECUTE FUNCTION nerocd_storage_copy_admission();
CREATE TRIGGER hosting_storage_copy_transfer BEFORE INSERT ON hosting_storage_backup_transfers FOR EACH ROW EXECUTE FUNCTION nerocd_storage_copy_admission();
DO $$ DECLARE owner_schema text:=current_schema(); routine text; BEGIN
 FOREACH routine IN ARRAY ARRAY['nerocd_storage_copy_admission()','nerocd_storage_retention_identity()','nerocd_storage_retention_lease()','nerocd_storage_retention_receipt()'] LOOP EXECUTE format('ALTER FUNCTION %I.%s SET search_path=%I,pg_catalog,pg_temp',owner_schema,routine,owner_schema); END LOOP;
END $$;

-- Proof-bearing generations require the exact original publication receipt.
-- Old receipts without these additive fields never acquire deletion ownership.
CREATE FUNCTION nerocd_storage_copy_proof() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE proof jsonb; transfer text;
BEGIN
 IF NEW.body->'local' IS NOT NULL AND NEW.location='local' THEN
  proof:=NEW.body->'local';
  IF NEW.id LIKE 'sbc_download_%' THEN
   transfer:=substring(NEW.id from length('sbc_download_')+1);
   IF NOT EXISTS(SELECT 1 FROM hosting_storage_backup_transfers t JOIN hosting_storage_backup_transfer_receipts r ON r.operation_id=t.id AND r.phase='complete' WHERE t.id=transfer AND t.backup_id=NEW.backup_id AND t.mode='download' AND r.body->'local_copy'=proof AND t.blob->>'sha256'=NEW.body->'item'->>'digest' AND t.blob->>'size_bytes'=NEW.body->'item'->>'bytes') THEN RAISE EXCEPTION 'storage downloaded proof requires receipt' USING ERRCODE='23514'; END IF;
  ELSIF NOT EXISTS(SELECT 1 FROM hosting_storage_backups b JOIN hosting_storage_archive_receipts r ON r.operation_id=b.operation_id AND r.phase='archive_published' WHERE b.id=NEW.backup_id AND r.body->'local_copy'=proof AND r.body->'blob'->>'sha256'=NEW.body->'item'->>'digest' AND r.body->'blob'->>'size_bytes'=NEW.body->'item'->>'bytes') THEN RAISE EXCEPTION 'storage local proof requires receipt' USING ERRCODE='23514'; END IF;
 ELSIF NEW.body->'remote' IS NOT NULL AND NEW.location='remote' THEN
  IF NOT EXISTS(SELECT 1 FROM hosting_storage_backup_transfers t JOIN hosting_storage_backup_transfer_receipts r ON r.operation_id=t.id AND r.phase='complete' WHERE t.id=NEW.transfer_id AND t.backup_id=NEW.backup_id AND t.mode='upload' AND r.body->'remote_copy'=NEW.body->'remote' AND t.blob->>'sha256'=NEW.body->'item'->>'digest' AND t.blob->>'size_bytes'=NEW.body->'item'->>'bytes') THEN RAISE EXCEPTION 'storage remote proof requires receipt' USING ERRCODE='23514'; END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE CONSTRAINT TRIGGER hosting_storage_copy_proof AFTER INSERT ON hosting_storage_backup_copies DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION nerocd_storage_copy_proof();
CREATE FUNCTION nerocd_storage_copy_supersession() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.superseded_by_copy_id IS DISTINCT FROM OLD.superseded_by_copy_id THEN
  IF OLD.superseded_by_copy_id IS NOT NULL OR OLD.location<>'local' OR OLD.availability NOT IN ('available','unproven','deleted') OR NEW.availability<>OLD.availability OR NEW.superseded_by_copy_id NOT LIKE 'sbc_download_%' OR EXISTS(SELECT 1 FROM hosting_storage_backup_retention_items i WHERE i.copy_id=OLD.id AND i.reservation_held) THEN RAISE EXCEPTION 'invalid storage copy supersession' USING ERRCODE='23514'; END IF;
  IF NOT EXISTS(SELECT 1 FROM hosting_storage_backup_copies n WHERE n.id=NEW.superseded_by_copy_id AND n.backup_id=NEW.backup_id AND n.resource_id=NEW.resource_id AND n.runner_id=NEW.runner_id AND n.location='local' AND n.superseded_by_copy_id IS NULL AND n.body->'local'->>'created'='true' AND n.body->'item'->>'digest'=NEW.body->'item'->>'digest' AND n.body->'item'->>'bytes'=NEW.body->'item'->>'bytes' AND n.body->>'server_id'=NEW.body->>'server_id' AND n.body->>'destination_id'=NEW.body->>'destination_id' AND n.body->>'daemon_id'=NEW.body->>'daemon_id') THEN RAISE EXCEPTION 'storage copy supersession requires fresh generation' USING ERRCODE='23514'; END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE CONSTRAINT TRIGGER hosting_storage_copy_supersession AFTER UPDATE ON hosting_storage_backup_copies DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION nerocd_storage_copy_supersession();
DO $$ DECLARE owner_schema text:=current_schema(); BEGIN
 EXECUTE format('ALTER FUNCTION %I.nerocd_storage_copy_proof() SET search_path=%I,pg_catalog,pg_temp',owner_schema,owner_schema);
 EXECUTE format('ALTER FUNCTION %I.nerocd_storage_copy_supersession() SET search_path=%I,pg_catalog,pg_temp',owner_schema,owner_schema);
END $$;

-- Current-authority validation may already have flushed all constraints. Defer
-- only the new copy links until their original completion receipt is inserted.
CREATE FUNCTION nerocd_storage_retention_defer_copy() RETURNS void LANGUAGE plpgsql AS $$
BEGIN
 SET CONSTRAINTS hosting_storage_copy_proof,hosting_storage_copy_supersession,hosting_storage_backup_copies_superseded_by_copy_id_fkey DEFERRED;
END $$;
DO $$ DECLARE owner_schema text:=current_schema(); BEGIN
 EXECUTE format('ALTER FUNCTION %I.nerocd_storage_retention_defer_copy() SET search_path=%I,pg_catalog,pg_temp',owner_schema,owner_schema);
END $$;
