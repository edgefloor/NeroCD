-- Remote object transfer has separate fenced work. A local backup receipt never
-- depends on mutable upload progress, and upload owns no live database binding.
CREATE TABLE hosting_database_backup_targets (
 id text PRIMARY KEY,
 project_id text NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
 revision bigint NOT NULL CHECK(revision>0),
 enabled boolean NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(id,project_id)
);
CREATE TABLE hosting_database_backup_remote_credentials (
 id text PRIMARY KEY,
 project_id text NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
 version bigint NOT NULL CHECK(version=1),
 status text NOT NULL CHECK(status IN ('active','revoked')),
 envelope_version integer NOT NULL,
 key_id text NOT NULL,
 ciphertext bytea NOT NULL,
 created_by text NOT NULL REFERENCES principals(id) ON DELETE RESTRICT,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE hosting_database_backup_target_versions (
 target_id text NOT NULL REFERENCES hosting_database_backup_targets(id) ON DELETE RESTRICT,
 revision bigint NOT NULL CHECK(revision>0),
 actor_id text NOT NULL REFERENCES principals(id) ON DELETE RESTRICT,
 snapshot jsonb NOT NULL CHECK(jsonb_typeof(snapshot)='object'),
 access_id text NOT NULL REFERENCES hosting_database_backup_remote_credentials(id) ON DELETE RESTRICT,
 secret_id text NOT NULL REFERENCES hosting_database_backup_remote_credentials(id) ON DELETE RESTRICT,
 PRIMARY KEY(target_id,revision), CHECK(access_id<>secret_id)
);
CREATE TABLE hosting_database_backup_target_requests (
 project_id text NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
 request_id text NOT NULL,
 actor_id text NOT NULL REFERENCES principals(id) ON DELETE RESTRICT,
 request_hash text NOT NULL CHECK(request_hash ~ '^sha256:[a-f0-9]{64}$'),
 target_id text NOT NULL,
 revision bigint NOT NULL,
 credentials_supplied boolean NOT NULL,
 response jsonb NOT NULL CHECK(jsonb_typeof(response)='object'),
 PRIMARY KEY(project_id,request_id),
 FOREIGN KEY(target_id,revision) REFERENCES hosting_database_backup_target_versions(target_id,revision) ON DELETE RESTRICT
);
CREATE TABLE hosting_database_backup_remote_selections (
 operation_id text PRIMARY KEY REFERENCES hosting_database_backups(id) ON DELETE RESTRICT,
 target_id text NOT NULL,
 target_revision bigint NOT NULL,
 FOREIGN KEY(target_id,target_revision) REFERENCES hosting_database_backup_target_versions(target_id,revision) ON DELETE RESTRICT
);
CREATE TABLE hosting_database_backup_schedule_remote_targets (
 schedule_id text NOT NULL,
 schedule_revision bigint NOT NULL,
 target_id text NOT NULL,
 target_revision bigint NOT NULL,
 PRIMARY KEY(schedule_id,schedule_revision),
 FOREIGN KEY(schedule_id,schedule_revision) REFERENCES hosting_database_backup_schedule_versions(schedule_id,revision) ON DELETE RESTRICT,
 FOREIGN KEY(target_id,target_revision) REFERENCES hosting_database_backup_target_versions(target_id,revision) ON DELETE RESTRICT
);
CREATE TRIGGER hosting_database_backup_schedule_remote_targets_immutable BEFORE UPDATE OR DELETE ON hosting_database_backup_schedule_remote_targets FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_execution_immutable();
CREATE TABLE hosting_database_backup_transfers (
 id text PRIMARY KEY,
 project_id text NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
 target_id text NOT NULL,
 target_revision bigint NOT NULL,
 destination_id text NOT NULL REFERENCES destinations(id) ON DELETE RESTRICT,
 actor_id text NOT NULL REFERENCES principals(id) ON DELETE RESTRICT,
 request_id text NOT NULL,
 request_hash text NOT NULL CHECK(request_hash ~ '^sha256:[a-f0-9]{64}$'),
 mode text NOT NULL CHECK(mode IN ('validate','upload','download')),
 backup_id text REFERENCES hosting_database_backups(id) ON DELETE RESTRICT,
 restore_operation_id text UNIQUE REFERENCES hosting_database_backups(id) ON DELETE RESTRICT,
 predecessor_id text REFERENCES hosting_database_backup_transfers(id) ON DELETE RESTRICT,
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
CREATE UNIQUE INDEX hosting_database_backup_first_upload ON hosting_database_backup_transfers(backup_id) WHERE mode='upload' AND predecessor_id IS NULL;
CREATE TABLE hosting_database_backup_transfer_receipts (
 operation_id text NOT NULL REFERENCES hosting_database_backup_transfers(id) ON DELETE RESTRICT,
 phase text NOT NULL CHECK(phase IN ('intent','complete','failed','uncertain')),
 receipt_id text NOT NULL UNIQUE,
 authority jsonb NOT NULL,
 body jsonb NOT NULL,
 body_hash text NOT NULL CHECK(body_hash ~ '^sha256:[a-f0-9]{64}$'),
 response jsonb NOT NULL,
	 expired_classification boolean NOT NULL DEFAULT false,
 PRIMARY KEY(operation_id,phase)
);
CREATE TABLE hosting_database_backup_remote_deliveries (
 delivery_id text PRIMARY KEY,
 operation_id text NOT NULL REFERENCES hosting_database_backup_transfers(id) ON DELETE RESTRICT,
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
CREATE TRIGGER hosting_database_backup_target_versions_immutable BEFORE UPDATE OR DELETE ON hosting_database_backup_target_versions FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_execution_immutable();
CREATE TRIGGER hosting_database_backup_target_requests_immutable BEFORE UPDATE OR DELETE ON hosting_database_backup_target_requests FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_execution_immutable();
CREATE TRIGGER hosting_database_backup_remote_selections_immutable BEFORE UPDATE OR DELETE ON hosting_database_backup_remote_selections FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_execution_immutable();
CREATE TRIGGER hosting_database_backup_transfer_receipts_immutable BEFORE UPDATE OR DELETE ON hosting_database_backup_transfer_receipts FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_execution_immutable();
CREATE TRIGGER hosting_database_backup_remote_deliveries_immutable BEFORE UPDATE OR DELETE ON hosting_database_backup_remote_deliveries FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_execution_immutable();
CREATE FUNCTION nerocd_backup_remote_target_identity() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' OR (NEW.id,NEW.project_id,NEW.created_at) IS DISTINCT FROM (OLD.id,OLD.project_id,OLD.created_at) OR NEW.revision NOT IN (OLD.revision,OLD.revision+1) OR (NEW.revision=OLD.revision AND NEW.enabled IS DISTINCT FROM OLD.enabled) THEN
  RAISE EXCEPTION 'backup remote target identity is immutable' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER hosting_database_backup_remote_target_identity BEFORE UPDATE OR DELETE ON hosting_database_backup_targets FOR EACH ROW EXECUTE FUNCTION nerocd_backup_remote_target_identity();
CREATE FUNCTION nerocd_backup_remote_target_version() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NOT EXISTS(SELECT 1 FROM hosting_database_backup_targets t JOIN hosting_database_backup_target_versions v ON v.target_id=t.id AND v.revision=t.revision WHERE t.id=NEW.id AND v.snapshot->'target'->>'id'=t.id AND v.snapshot->'target'->>'project_id'=t.project_id AND v.snapshot->'target'->>'revision'=t.revision::text AND v.snapshot->'target'->>'enabled'=t.enabled::text) THEN
  RAISE EXCEPTION 'backup remote target current version mismatch' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE CONSTRAINT TRIGGER hosting_database_backup_remote_target_version AFTER INSERT OR UPDATE ON hosting_database_backup_targets DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION nerocd_backup_remote_target_version();
CREATE FUNCTION nerocd_backup_remote_credential_identity() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' OR (to_jsonb(NEW)-'status') IS DISTINCT FROM (to_jsonb(OLD)-'status') OR OLD.status='revoked' THEN
  RAISE EXCEPTION 'backup remote credential identity is immutable' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER hosting_database_backup_remote_credential_identity BEFORE UPDATE OR DELETE ON hosting_database_backup_remote_credentials FOR EACH ROW EXECUTE FUNCTION nerocd_backup_remote_credential_identity();
CREATE FUNCTION nerocd_backup_remote_transfer_identity() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' OR
 (to_jsonb(NEW)-ARRAY['state','failure_code','run_id','daemon_id','updated_at']) IS DISTINCT FROM (to_jsonb(OLD)-ARRAY['state','failure_code','run_id','daemon_id','updated_at']) OR
 (OLD.run_id IS NOT NULL AND NEW.run_id IS DISTINCT FROM OLD.run_id) OR
 (OLD.daemon_id<>'' AND NEW.daemon_id<>OLD.daemon_id) OR
 (OLD.state IN ('succeeded','failed','uncertain') AND NEW IS DISTINCT FROM OLD) OR
 (NEW.state<>OLD.state AND NOT ((OLD.state='queued' AND NEW.state IN ('dispatched','failed')) OR (OLD.state='dispatched' AND NEW.state IN ('applying','failed')) OR (OLD.state='applying' AND NEW.state IN ('succeeded','failed','uncertain')))) THEN
  RAISE EXCEPTION 'backup remote transfer identity is immutable' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER hosting_database_backup_remote_transfer_identity BEFORE UPDATE OR DELETE ON hosting_database_backup_transfers FOR EACH ROW EXECUTE FUNCTION nerocd_backup_remote_transfer_identity();
CREATE FUNCTION nerocd_backup_remote_admission() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE snapshot jsonb; original hosting_database_backups; parent hosting_database_backup_transfers; restore hosting_database_backups;
BEGIN
 SELECT v.snapshot INTO snapshot FROM hosting_database_backup_target_versions v JOIN hosting_database_backup_targets t ON t.id=v.target_id WHERE v.target_id=NEW.target_id AND v.revision=NEW.target_revision AND t.project_id=NEW.project_id;
 IF snapshot IS NULL OR NEW.plan->'target' IS DISTINCT FROM snapshot OR NEW.plan->>'hash' IS DISTINCT FROM NEW.plan_hash OR NEW.plan->>'actor_id' IS DISTINCT FROM NEW.actor_id OR NEW.plan->>'runner_id' IS DISTINCT FROM NEW.runner_id OR NEW.plan->'operation'->>'id' IS DISTINCT FROM NEW.id OR NEW.plan->'operation'->>'project_id' IS DISTINCT FROM NEW.project_id OR NEW.plan->'operation'->>'target_id' IS DISTINCT FROM NEW.target_id OR NEW.plan->'operation'->>'target_revision' IS DISTINCT FROM NEW.target_revision::text OR NEW.plan->'operation'->>'destination_id' IS DISTINCT FROM NEW.destination_id OR NEW.plan->'operation'->>'mode' IS DISTINCT FROM NEW.mode OR NEW.state<>'queued' OR NEW.run_id IS NOT NULL OR NEW.failure_code<>'' THEN
  RAISE EXCEPTION 'remote backup admission identity mismatch' USING ERRCODE='23514';
 END IF;
 IF NEW.mode<>'validate' THEN
  SELECT * INTO original FROM hosting_database_backups WHERE id=NEW.backup_id;
  IF original.id IS NULL OR original.mode<>'backup' OR original.state<>'succeeded' OR original.project_id<>NEW.project_id OR original.blob IS DISTINCT FROM NEW.blob OR NEW.plan->'operation'->'blob' IS DISTINCT FROM NEW.blob OR NEW.plan->'operation'->>'backup_id' IS DISTINCT FROM NEW.backup_id OR original.plan->'source'->'snapshot'->>'destination_id' IS DISTINCT FROM NEW.destination_id OR original.runner_id<>NEW.runner_id OR original.plan->'source'->'release'->>'daemon_id' IS DISTINCT FROM NEW.daemon_id OR NEW.plan->>'daemon_id' IS DISTINCT FROM NEW.daemon_id OR NEW.plan->>'location_id' IS DISTINCT FROM NEW.backup_id OR NEW.plan->>'object_key' IS DISTINCT FROM ('nerocd/postgresql/'||NEW.backup_id||'/'||substring(NEW.blob->>'digest' from 8)||'.dump') THEN
   RAISE EXCEPTION 'remote backup blob identity mismatch' USING ERRCODE='23514';
  END IF;
 END IF;
 IF NEW.predecessor_id IS NOT NULL THEN
  SELECT * INTO parent FROM hosting_database_backup_transfers WHERE id=NEW.predecessor_id;
  IF parent.id IS NULL OR parent.mode<>'upload' OR parent.backup_id IS DISTINCT FROM NEW.backup_id OR parent.blob IS DISTINCT FROM NEW.blob OR parent.target_id<>NEW.target_id OR parent.target_revision<>NEW.target_revision OR parent.destination_id<>NEW.destination_id OR parent.runner_id<>NEW.runner_id OR parent.plan->>'object_key' IS DISTINCT FROM NEW.plan->>'object_key' OR parent.plan->'target' IS DISTINCT FROM NEW.plan->'target' OR (NEW.mode='upload' AND parent.state NOT IN ('failed','uncertain')) OR (NEW.mode='download' AND parent.state<>'succeeded') THEN
   RAISE EXCEPTION 'remote backup predecessor mismatch' USING ERRCODE='23514';
  END IF;
 END IF;
 IF NEW.mode='download' THEN
  SELECT * INTO restore FROM hosting_database_backups WHERE id=NEW.restore_operation_id;
  IF NEW.predecessor_id IS NULL OR restore.id IS NULL OR restore.mode<>'restore' OR NOT restore.ownership_held OR restore.state<>'waiting_target' OR restore.backup_id<>NEW.backup_id OR restore.project_id<>NEW.project_id OR NEW.plan->'operation'->>'restore_operation_id' IS DISTINCT FROM restore.id THEN
   RAISE EXCEPTION 'remote backup download restore mismatch' USING ERRCODE='23514';
  END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER hosting_database_backup_remote_admission BEFORE INSERT ON hosting_database_backup_transfers FOR EACH ROW EXECUTE FUNCTION nerocd_backup_remote_admission();
CREATE FUNCTION nerocd_backup_remote_lease() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE b hosting_database_backup_transfers; r runners; spec jsonb;
BEGIN
 SELECT run_spec INTO spec FROM task_runs WHERE id=NEW.run_id;
 IF spec->>'type' IS DISTINCT FROM 'database_backup_transfer' THEN RETURN NEW; END IF;
 SELECT * INTO b FROM hosting_database_backup_transfers WHERE run_id=NEW.run_id;
 SELECT * INTO r FROM runners WHERE id=NEW.runner_id;
 IF b.id IS NULL OR b.runner_id IS DISTINCT FROM r.id OR b.state<>'dispatched' OR r.status<>'active' OR r.token_hash='' OR NOT('database_backup_transfer'=ANY(r.capabilities)) OR NOT('hosting_postgresql_remote_backup_v1'=ANY(r.capabilities)) OR 'shell'=ANY(r.capabilities) OR spec<>jsonb_build_object('type','database_backup_transfer','inputs','{}'::jsonb,'hosting_operation_id',b.id,'hosting_mode',b.mode) OR EXISTS(SELECT 1 FROM run_leases WHERE run_id=NEW.run_id) OR NOT EXISTS(SELECT 1 FROM runner_enrollments e WHERE e.runner_id=r.id AND e.used_at IS NOT NULL AND e.revoked_at IS NULL AND e.credential_hash=r.token_hash) THEN
  RAISE EXCEPTION 'remote backup lease identity mismatch' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER hosting_database_backup_remote_lease BEFORE INSERT ON run_leases FOR EACH ROW EXECUTE FUNCTION nerocd_backup_remote_lease();
CREATE FUNCTION nerocd_backup_remote_receipt() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE b hosting_database_backup_transfers; l run_leases; a jsonb:=NEW.authority;
BEGIN
 SELECT * INTO b FROM hosting_database_backup_transfers WHERE id=NEW.operation_id;
 SELECT * INTO l FROM run_leases WHERE id=a->>'lease_id';
 IF b.id IS NULL OR l.id IS NULL OR b.run_id IS DISTINCT FROM l.run_id OR b.runner_id IS DISTINCT FROM l.runner_id OR a->>'operation_id' IS DISTINCT FROM b.id OR a->>'run_id' IS DISTINCT FROM b.run_id OR a->>'attempt' IS DISTINCT FROM l.attempt::text OR a->>'fence' IS DISTINCT FROM l.fence OR NEW.body->>'receipt_id' IS DISTINCT FROM NEW.receipt_id OR NEW.body->>'plan_hash' IS DISTINCT FROM b.plan_hash OR NEW.body->>'phase' IS DISTINCT FROM NEW.phase OR NEW.body - ARRAY['receipt_id','plan_hash','phase','failure_code','daemon_id'] IS DISTINCT FROM a OR NEW.body->>'daemon_id' IS DISTINCT FROM b.daemon_id OR NEW.response->>'id' IS DISTINCT FROM b.id OR NEW.response->>'state' IS DISTINCT FROM b.state OR NEW.response->>'failure_code' IS DISTINCT FROM b.failure_code OR NEW.response->'blob' IS DISTINCT FROM b.blob THEN
  RAISE EXCEPTION 'remote backup receipt identity mismatch' USING ERRCODE='23514';
 END IF;
 IF NEW.expired_classification THEN
  IF b.mode<>'upload' OR b.state<>'uncertain' OR NEW.phase<>'uncertain' OR NEW.body->>'failure_code'<>'interrupted' OR l.expires_at>clock_timestamp() OR l.status NOT IN ('active','expired') OR EXISTS(SELECT 1 FROM run_leases n WHERE n.run_id=l.run_id AND n.attempt>l.attempt) OR NOT EXISTS(SELECT 1 FROM hosting_database_backup_transfer_receipts i WHERE i.operation_id=b.id AND i.phase='intent' AND i.authority=a AND i.body->>'plan_hash'=b.plan_hash AND i.body->>'daemon_id'=b.daemon_id) THEN RAISE EXCEPTION 'remote backup expired classification mismatch' USING ERRCODE='23514'; END IF;
 ELSIF l.expires_at<=clock_timestamp() OR (NEW.phase='intent' AND (b.state<>'applying' OR l.status<>'active')) OR (NEW.phase='complete' AND (b.state<>'succeeded' OR l.status<>'succeeded')) OR (NEW.phase='failed' AND (b.state<>'failed' OR l.status<>'failed')) OR (NEW.phase='uncertain' AND (b.state<>'uncertain' OR l.status<>'failed')) THEN
  RAISE EXCEPTION 'remote backup receipt settlement mismatch' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER hosting_database_backup_remote_receipt BEFORE INSERT ON hosting_database_backup_transfer_receipts FOR EACH ROW EXECUTE FUNCTION nerocd_backup_remote_receipt();
DO $$ DECLARE owner_schema text:=current_schema(); routine text; BEGIN
 FOREACH routine IN ARRAY ARRAY['nerocd_backup_remote_target_identity()','nerocd_backup_remote_target_version()','nerocd_backup_remote_credential_identity()','nerocd_backup_remote_transfer_identity()','nerocd_backup_remote_admission()','nerocd_backup_remote_lease()','nerocd_backup_remote_receipt()'] LOOP EXECUTE format('ALTER FUNCTION %I.%s SET search_path=%I,pg_catalog,pg_temp',owner_schema,routine,owner_schema); END LOOP;
END $$;
