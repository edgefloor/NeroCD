CREATE FUNCTION nerocd_backup_remote_scope(endpoint text,bucket text,object_key text) RETURNS text LANGUAGE sql IMMUTABLE STRICT AS $$
 SELECT lower(regexp_replace(rtrim(endpoint,'/'),CASE WHEN endpoint LIKE 'https://%' THEN ':443$' ELSE ':80$' END,''))||'/'||bucket||'/'||object_key
$$;
-- Copy availability is separate from immutable logical backup history.
CREATE TABLE hosting_database_backup_retention_policies (
 resource_id text PRIMARY KEY REFERENCES resources(id) ON DELETE RESTRICT,
 project_id text NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
 actor_id text NOT NULL REFERENCES principals(id) ON DELETE RESTRICT,
 revision bigint NOT NULL CHECK(revision>0),
 body jsonb NOT NULL CHECK(jsonb_typeof(body)='object')
);
CREATE TABLE hosting_database_backup_retention_requests (
 resource_id text NOT NULL REFERENCES resources(id) ON DELETE RESTRICT,
 request_id text NOT NULL, kind text NOT NULL CHECK(kind IN ('policy','preview','execution')),
 actor_id text NOT NULL REFERENCES principals(id) ON DELETE RESTRICT,
 request_hash text NOT NULL, response jsonb NOT NULL,
 PRIMARY KEY(resource_id,kind,request_id)
);
CREATE TRIGGER hosting_database_backup_retention_requests_immutable BEFORE UPDATE OR DELETE ON hosting_database_backup_retention_requests FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_execution_immutable();
CREATE TABLE hosting_database_backup_copies (
 id text PRIMARY KEY,
 backup_id text NOT NULL REFERENCES hosting_database_backups(id) ON DELETE RESTRICT,
 transfer_id text REFERENCES hosting_database_backup_transfers(id) ON DELETE RESTRICT,
 resource_id text NOT NULL REFERENCES resources(id) ON DELETE RESTRICT,
 runner_id text NOT NULL REFERENCES runners(id) ON DELETE RESTRICT,
 location text NOT NULL CHECK(location IN ('local','remote')),
 identity_hash text NOT NULL,
 location_hash text NOT NULL,
 body jsonb NOT NULL CHECK(jsonb_typeof(body)='object'),
 availability text NOT NULL CHECK(availability IN ('available','reserved','deleted','uncertain','unproven')),
 created_at timestamptz NOT NULL
);
CREATE UNIQUE INDEX hosting_database_backup_copy_current_location ON hosting_database_backup_copies(backup_id,location,location_hash) WHERE availability<>'deleted';
CREATE INDEX hosting_database_backup_copy_identity ON hosting_database_backup_copies(identity_hash);
CREATE INDEX hosting_database_backup_copy_location ON hosting_database_backup_copies(location_hash);
CREATE TABLE hosting_database_backup_retention_previews (
 id text PRIMARY KEY,resource_id text NOT NULL REFERENCES resources(id) ON DELETE RESTRICT,
 actor_id text NOT NULL REFERENCES principals(id) ON DELETE RESTRICT,
 policy_revision bigint NOT NULL,body jsonb NOT NULL,copies jsonb NOT NULL,
 plan_hash text NOT NULL CHECK(plan_hash ~ '^sha256:[a-f0-9]{64}$')
);
CREATE TRIGGER hosting_database_backup_retention_previews_immutable BEFORE UPDATE OR DELETE ON hosting_database_backup_retention_previews FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_execution_immutable();
CREATE TABLE hosting_database_backup_retention_executions (
 id text PRIMARY KEY, resource_id text NOT NULL REFERENCES resources(id) ON DELETE RESTRICT,
 actor_id text NOT NULL REFERENCES principals(id) ON DELETE RESTRICT,
 runner_id text NOT NULL REFERENCES runners(id) ON DELETE RESTRICT,
 preview_id text NOT NULL UNIQUE REFERENCES hosting_database_backup_retention_previews(id) ON DELETE RESTRICT,
 run_id text UNIQUE REFERENCES task_runs(id) ON DELETE RESTRICT,
 plan jsonb NOT NULL,plan_hash text NOT NULL CHECK(plan_hash ~ '^sha256:[a-f0-9]{64}$'),
 body jsonb NOT NULL,reservation_held boolean NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE hosting_database_backup_retention_items (
 execution_id text NOT NULL REFERENCES hosting_database_backup_retention_executions(id) ON DELETE RESTRICT,
 copy_id text NOT NULL REFERENCES hosting_database_backup_copies(id) ON DELETE RESTRICT,
 identity_hash text NOT NULL, location_hash text NOT NULL,
 state text NOT NULL CHECK(state IN ('pending','intent','deleted','absent','failed','uncertain')),
 reservation_held boolean NOT NULL,
 PRIMARY KEY(execution_id,copy_id)
);
CREATE UNIQUE INDEX hosting_database_backup_retention_reserved_identity ON hosting_database_backup_retention_items(identity_hash) WHERE reservation_held;
CREATE UNIQUE INDEX hosting_database_backup_retention_reserved_location ON hosting_database_backup_retention_items(location_hash) WHERE reservation_held;
CREATE TABLE hosting_database_backup_retention_receipts (
 execution_id text NOT NULL REFERENCES hosting_database_backup_retention_executions(id) ON DELETE RESTRICT,
 copy_id text NOT NULL,phase text NOT NULL,attempt integer NOT NULL CHECK(attempt>0),receipt_id text NOT NULL UNIQUE,
 authority jsonb NOT NULL,body jsonb NOT NULL,body_hash text NOT NULL,response jsonb NOT NULL,
 PRIMARY KEY(execution_id,copy_id,phase,attempt),
 FOREIGN KEY(execution_id,copy_id) REFERENCES hosting_database_backup_retention_items(execution_id,copy_id) ON DELETE RESTRICT
);
CREATE TRIGGER hosting_database_backup_retention_receipts_immutable BEFORE UPDATE OR DELETE ON hosting_database_backup_retention_receipts FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_execution_immutable();
-- The same lock is acquired by retention selection/admission and every archive
-- producer/consumer admission. Active work is examined before reserving a pool.
CREATE FUNCTION nerocd_backup_copy_admission() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE server_key text;

BEGIN
 PERFORM pg_advisory_xact_lock(hashtextextended('nerocd-backup-copy-admission',0));
 IF TG_TABLE_NAME='hosting_database_backups' THEN server_key:=NEW.plan->'source'->'snapshot'->>'server_id'; ELSE server_key:=NEW.plan->>'server_id'; END IF;
 IF EXISTS(SELECT 1 FROM hosting_database_backup_retention_executions e WHERE e.reservation_held AND (e.runner_id=NEW.runner_id OR e.plan->'copies'->0->>'server_id'=server_key)) THEN
  RAISE EXCEPTION 'backup archive pool reserved' USING ERRCODE='23514';
 END IF;
 IF TG_TABLE_NAME='hosting_database_backup_transfers' AND NEW.mode IN ('upload','download') THEN
  IF EXISTS(SELECT 1 FROM hosting_database_backup_retention_items i JOIN hosting_database_backup_copies c ON c.id=i.copy_id WHERE i.reservation_held AND c.location='remote' AND nerocd_backup_remote_scope(c.body->'target'->'target'->>'endpoint',c.body->'target'->'target'->>'bucket',c.body->>'object_key')=nerocd_backup_remote_scope(NEW.plan->'target'->'target'->>'endpoint',NEW.plan->'target'->'target'->>'bucket',NEW.plan->>'object_key')) THEN
   RAISE EXCEPTION 'backup remote object reserved' USING ERRCODE='23514';
  END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER hosting_backup_copy_producer BEFORE INSERT ON hosting_database_backups FOR EACH ROW EXECUTE FUNCTION nerocd_backup_copy_admission();
CREATE TRIGGER hosting_backup_copy_transfer BEFORE INSERT ON hosting_database_backup_transfers FOR EACH ROW EXECUTE FUNCTION nerocd_backup_copy_admission();
CREATE TABLE hosting_database_backup_retention_deliveries (
 delivery_id text PRIMARY KEY,
 execution_id text NOT NULL REFERENCES hosting_database_backup_retention_executions(id) ON DELETE RESTRICT,
 copy_id text NOT NULL,
 metadata jsonb NOT NULL,use_until timestamptz NOT NULL,
 FOREIGN KEY(execution_id,copy_id) REFERENCES hosting_database_backup_retention_items(execution_id,copy_id) ON DELETE RESTRICT
);
CREATE TRIGGER hosting_database_backup_retention_deliveries_immutable BEFORE UPDATE OR DELETE ON hosting_database_backup_retention_deliveries FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_execution_immutable();

-- Additive proof fields preserve every existing receipt authority predicate.
CREATE OR REPLACE FUNCTION nerocd_database_backup_receipt() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE b hosting_database_backups; l run_leases; a jsonb:=NEW.authority;
BEGIN
 SELECT * INTO b FROM hosting_database_backups WHERE id=NEW.operation_id;
 SELECT * INTO l FROM run_leases WHERE id=a->>'lease_id';
 IF b.id IS NULL OR l.id IS NULL OR b.run_id IS DISTINCT FROM l.run_id OR b.runner_id IS DISTINCT FROM l.runner_id OR a->>'operation_id' IS DISTINCT FROM b.id OR a->>'run_id' IS DISTINCT FROM b.run_id OR a->>'attempt' IS DISTINCT FROM l.attempt::text OR a->>'fence' IS DISTINCT FROM l.fence OR NEW.body->>'receipt_id' IS DISTINCT FROM NEW.receipt_id OR NEW.body->>'plan_hash' IS DISTINCT FROM b.plan_hash OR NEW.body->>'phase' IS DISTINCT FROM NEW.phase OR NEW.body - ARRAY['receipt_id','plan_hash','phase','blob','failure_code','local_copy'] IS DISTINCT FROM a OR l.expires_at<=clock_timestamp() OR NEW.response->>'id' IS DISTINCT FROM b.id OR NEW.response->>'state' IS DISTINCT FROM b.state OR NEW.response->'blob' IS DISTINCT FROM b.blob THEN RAISE EXCEPTION 'database backup receipt authority mismatch' USING ERRCODE='23514'; END IF;
 IF (NEW.phase='intent' AND (b.state<>'applying' OR l.status<>'active' OR NOT b.ownership_held)) OR (NEW.phase='complete' AND (b.state<>'succeeded' OR l.status<>'succeeded' OR b.ownership_held OR NEW.body->'blob' IS DISTINCT FROM b.blob)) OR (NEW.phase='failed' AND (b.state<>'failed' OR l.status<>'failed' OR b.ownership_held IS DISTINCT FROM (b.mode='restore'))) OR (NEW.phase='uncertain' AND (b.state<>'uncertain' OR l.status<>'failed' OR NOT b.ownership_held)) THEN RAISE EXCEPTION 'database backup receipt settlement mismatch' USING ERRCODE='23514'; END IF;
 IF NEW.body ? 'local_copy' AND (b.mode<>'backup' OR NEW.phase<>'complete' OR NOT EXISTS(SELECT 1 FROM hosting_database_backup_copies c WHERE c.backup_id=b.id AND c.location='local' AND (c.body->'local'=NEW.body->'local_copy' OR (NEW.body->'local_copy'->>'created'='false' AND c.body->'local'=jsonb_set(NEW.body->'local_copy','{created}','true'::jsonb))) AND c.body->'item'->>'digest'=b.blob->>'digest')) THEN RAISE EXCEPTION 'database backup copy proof mismatch' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;

CREATE OR REPLACE FUNCTION nerocd_backup_remote_receipt() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE b hosting_database_backup_transfers; l run_leases; a jsonb:=NEW.authority;
BEGIN
 SELECT * INTO b FROM hosting_database_backup_transfers WHERE id=NEW.operation_id;
 SELECT * INTO l FROM run_leases WHERE id=a->>'lease_id';
 IF b.id IS NULL OR l.id IS NULL OR b.run_id IS DISTINCT FROM l.run_id OR b.runner_id IS DISTINCT FROM l.runner_id OR a->>'operation_id' IS DISTINCT FROM b.id OR a->>'run_id' IS DISTINCT FROM b.run_id OR a->>'attempt' IS DISTINCT FROM l.attempt::text OR a->>'fence' IS DISTINCT FROM l.fence OR NEW.body->>'receipt_id' IS DISTINCT FROM NEW.receipt_id OR NEW.body->>'plan_hash' IS DISTINCT FROM b.plan_hash OR NEW.body->>'phase' IS DISTINCT FROM NEW.phase OR NEW.body - ARRAY['receipt_id','plan_hash','phase','failure_code','daemon_id','remote_copy','local_copy'] IS DISTINCT FROM a OR NEW.body->>'daemon_id' IS DISTINCT FROM b.daemon_id OR NEW.response->>'id' IS DISTINCT FROM b.id OR NEW.response->>'state' IS DISTINCT FROM b.state OR NEW.response->>'failure_code' IS DISTINCT FROM b.failure_code OR NEW.response->'blob' IS DISTINCT FROM b.blob THEN
  RAISE EXCEPTION 'remote backup receipt identity mismatch' USING ERRCODE='23514';
 END IF;
 IF NEW.expired_classification THEN
  IF b.mode<>'upload' OR b.state<>'uncertain' OR NEW.phase<>'uncertain' OR NEW.body->>'failure_code'<>'interrupted' OR l.expires_at>clock_timestamp() OR l.status NOT IN ('active','expired') OR EXISTS(SELECT 1 FROM run_leases n WHERE n.run_id=l.run_id AND n.attempt>l.attempt) OR NOT EXISTS(SELECT 1 FROM hosting_database_backup_transfer_receipts i WHERE i.operation_id=b.id AND i.phase='intent' AND i.authority=a AND i.body->>'plan_hash'=b.plan_hash AND i.body->>'daemon_id'=b.daemon_id) THEN RAISE EXCEPTION 'remote backup expired classification mismatch' USING ERRCODE='23514'; END IF;
 ELSIF l.expires_at<=clock_timestamp() OR (NEW.phase='intent' AND (b.state<>'applying' OR l.status<>'active')) OR (NEW.phase='complete' AND (b.state<>'succeeded' OR l.status<>'succeeded')) OR (NEW.phase='failed' AND (b.state<>'failed' OR l.status<>'failed')) OR (NEW.phase='uncertain' AND (b.state<>'uncertain' OR l.status<>'failed')) THEN
  RAISE EXCEPTION 'remote backup receipt settlement mismatch' USING ERRCODE='23514';
 END IF;
 IF NEW.body ? 'remote_copy' AND (b.mode<>'upload' OR NEW.phase<>'complete' OR NOT EXISTS(SELECT 1 FROM hosting_database_backup_copies c WHERE c.transfer_id=b.id AND c.location='remote' AND (c.body->'remote'=NEW.body->'remote_copy' OR (NEW.body->'remote_copy'->>'created'='false' AND c.body->'remote'=jsonb_set(NEW.body->'remote_copy','{created}','true'::jsonb))))) THEN RAISE EXCEPTION 'database backup remote copy proof mismatch' USING ERRCODE='23514'; END IF;
 IF NEW.body ? 'local_copy' AND (b.mode<>'download' OR NEW.phase<>'complete' OR NEW.body ? 'remote_copy' OR NOT EXISTS(SELECT 1 FROM hosting_database_backup_copies c WHERE c.availability<>'deleted' AND c.backup_id=b.backup_id AND c.runner_id=b.runner_id AND c.location='local' AND (c.body->'local'=NEW.body->'local_copy' OR (NEW.body->'local_copy'->>'created'='false' AND c.body->'local'=jsonb_set(NEW.body->'local_copy','{created}','true'::jsonb))) AND c.body->'item'->>'digest'=b.blob->>'digest' AND c.body->>'server_id'=b.plan->>'server_id' AND c.body->>'destination_id'=b.destination_id)) THEN RAISE EXCEPTION 'database backup downloaded copy proof mismatch' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TABLE hosting_database_backup_retention_publications (
 id text PRIMARY KEY,resource_id text NOT NULL REFERENCES resources(id) ON DELETE RESTRICT,
 actor_id text NOT NULL REFERENCES principals(id) ON DELETE RESTRICT,
 policy_revision bigint NOT NULL,
 state text NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','admitted','skipped','denied')),
 reason text NOT NULL DEFAULT '' CHECK(reason IN ('','no_eligible_copy','authority_denied','scope_changed')),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
-- Immutable copy/selection identities are never rewritten to select a retry's
-- next batch, redirect an endpoint, or resurrect a deleted physical copy.
CREATE FUNCTION nerocd_backup_retention_identity() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'retention history is immutable' USING ERRCODE='23514'; END IF;
 IF TG_TABLE_NAME='hosting_database_backup_copies' THEN
  IF (NEW.id,NEW.backup_id,NEW.transfer_id,NEW.resource_id,NEW.runner_id,NEW.location,NEW.identity_hash,NEW.location_hash,NEW.body,NEW.created_at) IS DISTINCT FROM (OLD.id,OLD.backup_id,OLD.transfer_id,OLD.resource_id,OLD.runner_id,OLD.location,OLD.identity_hash,OLD.location_hash,OLD.body,OLD.created_at) OR (OLD.availability='deleted' AND NEW.availability<>'deleted') THEN RAISE EXCEPTION 'copy identity is immutable' USING ERRCODE='23514'; END IF;
 ELSIF TG_TABLE_NAME='hosting_database_backup_retention_executions' THEN
  IF (NEW.id,NEW.resource_id,NEW.actor_id,NEW.runner_id,NEW.preview_id,NEW.plan,NEW.plan_hash,NEW.created_at) IS DISTINCT FROM (OLD.id,OLD.resource_id,OLD.actor_id,OLD.runner_id,OLD.preview_id,OLD.plan,OLD.plan_hash,OLD.created_at) OR (OLD.run_id IS NOT NULL AND NEW.run_id IS DISTINCT FROM OLD.run_id) OR (NOT OLD.reservation_held AND NEW.reservation_held) THEN RAISE EXCEPTION 'retention execution identity is immutable' USING ERRCODE='23514'; END IF;
 ELSE
  IF (NEW.execution_id,NEW.copy_id,NEW.identity_hash,NEW.location_hash) IS DISTINCT FROM (OLD.execution_id,OLD.copy_id,OLD.identity_hash,OLD.location_hash) OR (NOT OLD.reservation_held AND NEW.reservation_held) THEN RAISE EXCEPTION 'retention item identity is immutable' USING ERRCODE='23514'; END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER hosting_database_backup_copy_identity BEFORE UPDATE OR DELETE ON hosting_database_backup_copies FOR EACH ROW EXECUTE FUNCTION nerocd_backup_retention_identity();
CREATE TRIGGER hosting_database_backup_retention_execution_identity BEFORE UPDATE OR DELETE ON hosting_database_backup_retention_executions FOR EACH ROW EXECUTE FUNCTION nerocd_backup_retention_identity();
CREATE TRIGGER hosting_database_backup_retention_item_identity BEFORE UPDATE OR DELETE ON hosting_database_backup_retention_items FOR EACH ROW EXECUTE FUNCTION nerocd_backup_retention_identity();
CREATE FUNCTION nerocd_backup_retention_lease() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE e hosting_database_backup_retention_executions; r runners; spec jsonb;
BEGIN
 SELECT run_spec INTO spec FROM task_runs WHERE id=NEW.run_id;
 IF spec->>'type' IS DISTINCT FROM 'database_backup_retention' THEN RETURN NEW; END IF;
 SELECT * INTO e FROM hosting_database_backup_retention_executions WHERE run_id=NEW.run_id;
 SELECT * INTO r FROM runners WHERE id=NEW.runner_id;
 IF e.id IS NULL OR e.runner_id<>r.id OR NOT e.reservation_held OR e.body->>'state'<>'dispatched' OR r.status<>'active' OR r.token_hash='' OR NOT('database_backup_retention'=ANY(r.capabilities)) OR NOT('hosting_postgresql_backup_retention_v1'=ANY(r.capabilities)) OR 'shell'=ANY(r.capabilities) OR spec<>jsonb_build_object('type','database_backup_retention','hosting_operation_id',e.id,'hosting_mode','retention','inputs','{}'::jsonb) OR (EXISTS(SELECT 1 FROM run_leases WHERE run_id=NEW.run_id) AND (NOT EXISTS(SELECT 1 FROM hosting_database_backup_retention_resumes a WHERE a.execution_id=e.id AND a.next_attempt=NEW.attempt AND a.plan_hash=e.plan_hash) OR EXISTS(SELECT 1 FROM run_leases prior_lease WHERE prior_lease.run_id=NEW.run_id AND prior_lease.status='active' AND prior_lease.expires_at>clock_timestamp()))) THEN RAISE EXCEPTION 'retention lease identity mismatch' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER hosting_database_backup_retention_lease BEFORE INSERT ON run_leases FOR EACH ROW EXECUTE FUNCTION nerocd_backup_retention_lease();
CREATE FUNCTION nerocd_backup_retention_receipt() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE e hosting_database_backup_retention_executions; l run_leases; i hosting_database_backup_retention_items;
BEGIN
 SELECT * INTO e FROM hosting_database_backup_retention_executions WHERE id=NEW.execution_id;
 SELECT * INTO l FROM run_leases WHERE id=NEW.authority->>'lease_id';
 SELECT * INTO i FROM hosting_database_backup_retention_items WHERE execution_id=NEW.execution_id AND copy_id=NEW.copy_id;
 IF e.id IS NULL OR l.id IS NULL OR i.copy_id IS NULL OR e.run_id IS DISTINCT FROM l.run_id OR e.runner_id IS DISTINCT FROM l.runner_id OR NEW.authority->>'operation_id' IS DISTINCT FROM e.id OR NEW.authority->>'run_id' IS DISTINCT FROM e.run_id OR NEW.authority->>'attempt' IS DISTINCT FROM l.attempt::text OR NEW.authority->>'fence' IS DISTINCT FROM l.fence OR l.expires_at<=clock_timestamp() OR NEW.response IS DISTINCT FROM e.body OR (NEW.phase NOT LIKE 'probe_%' AND NEW.phase IS DISTINCT FROM i.state) THEN RAISE EXCEPTION 'retention receipt authority mismatch' USING ERRCODE='23514'; END IF;
 IF NEW.attempt<>l.attempt OR NEW.body->>'receipt_id' IS DISTINCT FROM NEW.receipt_id OR NEW.body->>'copy_id' IS DISTINCT FROM NEW.copy_id OR NEW.body->>'plan_hash' IS DISTINCT FROM e.plan_hash OR NEW.body - ARRAY['receipt_id','copy_id','plan_hash','phase','failure_code','probe_only','probe_key'] IS DISTINCT FROM NEW.authority OR NEW.phase IS DISTINCT FROM (CASE WHEN NEW.body->>'probe_only'='true' THEN 'probe_'||(NEW.body->>'phase') ELSE NEW.body->>'phase' END) THEN RAISE EXCEPTION 'retention receipt body mismatch' USING ERRCODE='23514'; END IF;
 IF (e.body->>'state' IN ('dispatched','applying') AND l.status<>'active') OR (e.body->>'state'='succeeded' AND l.status<>'succeeded') OR (e.body->>'state' IN ('failed','uncertain') AND l.status<>'failed') THEN RAISE EXCEPTION 'retention receipt settlement mismatch' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER hosting_database_backup_retention_receipt BEFORE INSERT ON hosting_database_backup_retention_receipts FOR EACH ROW EXECUTE FUNCTION nerocd_backup_retention_receipt();
DO $$ DECLARE owner_schema text:=current_schema(); routine text; BEGIN
 FOREACH routine IN ARRAY ARRAY['nerocd_backup_copy_admission()','nerocd_backup_retention_identity()','nerocd_backup_retention_lease()','nerocd_backup_retention_receipt()','nerocd_database_backup_receipt()','nerocd_backup_remote_receipt()'] LOOP EXECUTE format('ALTER FUNCTION %I.%s SET search_path=%I,pg_catalog,pg_temp',owner_schema,routine,owner_schema); END LOOP;
END $$;
CREATE TABLE hosting_database_backup_retention_resumes (
 execution_id text NOT NULL REFERENCES hosting_database_backup_retention_executions(id) ON DELETE RESTRICT,
 next_attempt integer NOT NULL CHECK(next_attempt>1),
 request_id text NOT NULL,actor_id text NOT NULL REFERENCES principals(id) ON DELETE RESTRICT,
 plan_hash text NOT NULL,created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(execution_id,next_attempt),UNIQUE(execution_id,request_id)
);
CREATE TRIGGER hosting_database_backup_retention_resumes_immutable BEFORE UPDATE OR DELETE ON hosting_database_backup_retention_resumes FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_execution_immutable();

CREATE INDEX hosting_database_backup_retention_digest_refs ON hosting_database_backups((plan->'source'->'snapshot'->>'server_id'),(blob->>'digest')) WHERE mode='backup' AND state='succeeded';
CREATE INDEX hosting_database_backup_retention_remote_refs ON hosting_database_backup_transfers(nerocd_backup_remote_scope(plan->'target'->'target'->>'endpoint',plan->'target'->'target'->>'bucket',plan->>'object_key')) WHERE mode='upload' AND state='succeeded';
