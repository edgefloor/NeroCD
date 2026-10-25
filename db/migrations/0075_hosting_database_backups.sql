-- Hosted PostgreSQL logical snapshots are separate from platform backups and
-- upload archives. Locations and authority stay in runner-only immutable plans.
CREATE TABLE hosting_database_backups (
 id text PRIMARY KEY,
 project_id text NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
 source_resource_id text NOT NULL REFERENCES resources(id) ON DELETE RESTRICT,
 source_binding_id text NOT NULL REFERENCES resource_bindings(id) ON DELETE RESTRICT,
 source_generation bigint NOT NULL CHECK(source_generation>0),
 backup_id text NOT NULL REFERENCES hosting_database_backups(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
 mode text NOT NULL CHECK(mode IN ('backup','restore')),
 request_id text NOT NULL,
 request_hash text NOT NULL CHECK(request_hash ~ '^sha256:[a-f0-9]{64}$'),
 created_by text NOT NULL REFERENCES principals(id) ON DELETE RESTRICT,
 state text NOT NULL CHECK(state IN ('queued','waiting_target','dispatched','applying','succeeded','failed','uncertain')),
 target_resource_id text REFERENCES resources(id) ON DELETE RESTRICT,
 target_binding_id text REFERENCES resource_bindings(id) ON DELETE RESTRICT,
 deployment_operation_id text REFERENCES hosting_operations(id) ON DELETE RESTRICT,
 run_id text UNIQUE REFERENCES task_runs(id) ON DELETE RESTRICT,
 runner_id text NOT NULL REFERENCES runners(id) ON DELETE RESTRICT,
 plan jsonb NOT NULL CHECK(jsonb_typeof(plan)='object'),
 plan_hash text NOT NULL CHECK(plan_hash ~ '^sha256:[a-f0-9]{64}$'),
 blob jsonb CHECK(blob IS NULL OR jsonb_typeof(blob)='object'),
 failure_code text NOT NULL DEFAULT '' CHECK(failure_code IN ('','execution_failed','publication_failed','interrupted')),
 ownership_held boolean NOT NULL DEFAULT true,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(source_resource_id,mode,backup_id,request_id),
 CHECK((mode='backup' AND backup_id=id AND target_resource_id IS NULL AND target_binding_id IS NULL AND deployment_operation_id IS NULL) OR
       (mode='restore' AND backup_id<>id AND target_resource_id IS NOT NULL AND target_binding_id IS NOT NULL AND deployment_operation_id IS NOT NULL AND target_resource_id<>source_resource_id)),
 CHECK(state<>'succeeded' OR (blob IS NOT NULL AND NOT ownership_held)),
 CHECK(state<>'uncertain' OR ownership_held)
);
CREATE UNIQUE INDEX hosting_database_backup_request_identity ON hosting_database_backups(source_resource_id,request_id) WHERE mode='backup';
CREATE UNIQUE INDEX hosting_database_backup_source_writer ON hosting_database_backups(source_binding_id) WHERE ownership_held AND mode='backup';
CREATE UNIQUE INDEX hosting_database_backup_target_writer ON hosting_database_backups(target_binding_id) WHERE ownership_held;
CREATE TABLE hosting_database_backup_receipts (
 operation_id text NOT NULL REFERENCES hosting_database_backups(id) ON DELETE RESTRICT,
 phase text NOT NULL CHECK(phase IN ('intent','complete','failed','uncertain')),
 receipt_id text NOT NULL UNIQUE,
 authority jsonb NOT NULL,
 body jsonb NOT NULL,
 body_hash text NOT NULL CHECK(body_hash ~ '^sha256:[a-f0-9]{64}$'),
 response jsonb NOT NULL,
 PRIMARY KEY(operation_id,phase)
);
CREATE TRIGGER hosting_database_backup_receipts_immutable BEFORE UPDATE OR DELETE ON hosting_database_backup_receipts FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_execution_immutable();
CREATE FUNCTION nerocd_database_backup_admission() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE original hosting_database_backups; child hosting_operations; source hosting_operations;
BEGIN
 SELECT * INTO source FROM hosting_operations WHERE id=NEW.plan->'source'->>'operation_id';
 IF source.id IS NULL OR source.resource_id<>NEW.source_resource_id OR source.binding_id<>NEW.source_binding_id OR source.project_id<>NEW.project_id OR NEW.plan->>'actor_id' IS DISTINCT FROM NEW.created_by OR NEW.plan->>'runner_id' IS DISTINCT FROM NEW.runner_id OR NEW.plan->'operation'->>'id' IS DISTINCT FROM NEW.id OR NEW.plan->'operation'->>'backup_id' IS DISTINCT FROM NEW.backup_id OR NEW.plan->'operation'->>'mode' IS DISTINCT FROM NEW.mode OR NEW.plan->'source'->'snapshot'->>'resource_generation' IS DISTINCT FROM NEW.source_generation::text OR NEW.plan->>'location_id' IS DISTINCT FROM NEW.backup_id OR NEW.plan->>'hash' IS DISTINCT FROM NEW.plan_hash OR NEW.run_id IS NOT NULL OR NOT NEW.ownership_held OR NOT EXISTS(SELECT 1 FROM hosting_apply_plans p WHERE p.operation_id=source.id AND p.body=NEW.plan->'source_plan' AND p.body_hash=NEW.plan->'source'->>'plan_hash') THEN RAISE EXCEPTION 'database backup admission identity mismatch' USING ERRCODE='23514'; END IF;
 IF NEW.mode='backup' THEN
  IF NEW.state<>'queued' OR NEW.blob IS NOT NULL OR NOT EXISTS(SELECT 1 FROM hosting_binding_state b WHERE b.binding_id=NEW.source_binding_id AND b.healthy=NEW.plan->'source' AND b.hold_operation_id IS NULL AND b.restore_admitted) THEN RAISE EXCEPTION 'database backup source is not settled' USING ERRCODE='23514'; END IF;
 ELSE
  SELECT * INTO original FROM hosting_database_backups WHERE id=NEW.backup_id;
  SELECT * INTO child FROM hosting_operations WHERE id=NEW.deployment_operation_id;
  IF original.id IS NULL OR original.mode<>'backup' OR original.state<>'succeeded' OR original.ownership_held OR original.blob IS DISTINCT FROM NEW.blob OR original.project_id<>NEW.project_id OR original.source_resource_id<>NEW.source_resource_id OR original.source_binding_id<>NEW.source_binding_id OR original.runner_id<>NEW.runner_id OR NEW.state<>'waiting_target' OR child.id IS NULL OR child.resource_id IS DISTINCT FROM NEW.target_resource_id OR child.binding_id IS DISTINCT FROM NEW.target_binding_id OR child.created_by<>NEW.created_by OR child.project_id<>NEW.project_id OR NEW.plan->'source' IS DISTINCT FROM original.plan->'source' OR NEW.plan->'source_plan' IS DISTINCT FROM original.plan->'source_plan' OR NEW.plan->'source_storage' IS DISTINCT FROM original.plan->'source_storage' OR NEW.plan->'credentials' IS DISTINCT FROM original.plan->'credentials' THEN RAISE EXCEPTION 'database restore provenance or child mismatch' USING ERRCODE='23514'; END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER hosting_database_backup_admission BEFORE INSERT ON hosting_database_backups FOR EACH ROW EXECUTE FUNCTION nerocd_database_backup_admission();
CREATE FUNCTION nerocd_database_backup_identity() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' OR (NEW.id,NEW.project_id,NEW.source_resource_id,NEW.source_binding_id,NEW.source_generation,NEW.backup_id,NEW.mode,NEW.request_id,NEW.request_hash,NEW.created_by,NEW.target_resource_id,NEW.target_binding_id,NEW.deployment_operation_id,NEW.runner_id,NEW.created_at) IS DISTINCT FROM (OLD.id,OLD.project_id,OLD.source_resource_id,OLD.source_binding_id,OLD.source_generation,OLD.backup_id,OLD.mode,OLD.request_id,OLD.request_hash,OLD.created_by,OLD.target_resource_id,OLD.target_binding_id,OLD.deployment_operation_id,OLD.runner_id,OLD.created_at) OR (OLD.run_id IS NOT NULL AND (NEW.run_id,NEW.plan,NEW.plan_hash) IS DISTINCT FROM (OLD.run_id,OLD.plan,OLD.plan_hash)) OR (NOT OLD.ownership_held AND NEW.ownership_held) THEN RAISE EXCEPTION 'database backup identity is immutable' USING ERRCODE='23514'; END IF;
 IF (NEW.plan - ARRAY['target','target_plan','target_storage','hash']) IS DISTINCT FROM (OLD.plan - ARRAY['target','target_plan','target_storage','hash']) THEN RAISE EXCEPTION 'database backup source plan is immutable' USING ERRCODE='23514'; END IF;
 IF NEW.run_id IS NOT NULL AND OLD.run_id IS NULL AND NEW.mode='restore' AND (NEW.plan->'target'->>'operation_id' IS DISTINCT FROM NEW.deployment_operation_id OR NEW.plan->'target'->'snapshot'->>'resource_id' IS DISTINCT FROM NEW.target_resource_id OR NOT EXISTS(SELECT 1 FROM hosting_apply_plans p JOIN hosting_operations o ON o.id=p.operation_id JOIN hosting_binding_state b ON b.binding_id=o.binding_id WHERE o.id=NEW.deployment_operation_id AND o.state='succeeded' AND p.body=NEW.plan->'target_plan' AND b.healthy=NEW.plan->'target')) THEN RAISE EXCEPTION 'database restore dispatch requires exact settled child' USING ERRCODE='23514'; END IF;
 IF (OLD.blob IS NOT NULL AND NEW.blob IS DISTINCT FROM OLD.blob) OR (OLD.state IN ('succeeded','failed','uncertain') AND NEW IS DISTINCT FROM OLD) OR (NEW.state<>OLD.state AND NOT ((OLD.state='queued' AND NEW.state='dispatched') OR (OLD.state='waiting_target' AND NEW.state IN ('dispatched','uncertain')) OR (OLD.state='dispatched' AND NEW.state IN ('applying','failed')) OR (OLD.state='applying' AND NEW.state IN ('succeeded','uncertain')))) THEN RAISE EXCEPTION 'database backup transition or artifact changed' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER hosting_database_backup_identity BEFORE UPDATE OR DELETE ON hosting_database_backups FOR EACH ROW EXECUTE FUNCTION nerocd_database_backup_identity();
-- Exact persisted deploy child is the only mutation allowed through a target
-- restore hold. Connections cannot be admitted while import is held.
CREATE FUNCTION nerocd_database_backup_mutation_hold() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE binding_key text; child_key text;
BEGIN
 IF TG_TABLE_NAME='hosting_database_deployments' THEN binding_key:=NEW.binding_id; child_key:=NEW.operation_id;
 ELSIF TG_TABLE_NAME='hosting_database_bindings' THEN binding_key:=NEW.database_binding_id;
 ELSE RETURN NEW; END IF;
 IF EXISTS(SELECT 1 FROM hosting_database_backups b WHERE b.ownership_held AND ((b.mode='backup' AND b.source_binding_id=binding_key) OR (b.target_binding_id=binding_key AND b.deployment_operation_id IS DISTINCT FROM child_key))) THEN RAISE EXCEPTION 'database backup or restore owns target' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER hosting_database_backup_deploy_hold BEFORE INSERT ON hosting_database_deployments FOR EACH ROW EXECUTE FUNCTION nerocd_database_backup_mutation_hold();
CREATE TRIGGER hosting_database_backup_connection_hold BEFORE INSERT ON hosting_database_bindings FOR EACH ROW EXECUTE FUNCTION nerocd_database_backup_mutation_hold();
-- Revocation is always allowed; current-authority checks reject later effects.
-- Rotation cannot silently replace admitted credential versions during a job.
CREATE FUNCTION nerocd_database_backup_secret_hold() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.current_version<>OLD.current_version AND EXISTS(SELECT 1 FROM hosting_database_credentials c JOIN hosting_database_backups b ON (b.mode='backup' AND b.source_resource_id=c.resource_id) OR b.target_resource_id=c.resource_id WHERE c.secret_id=OLD.id AND b.ownership_held) THEN RAISE EXCEPTION 'database backup owns credential version' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER hosting_database_backup_secret_hold BEFORE UPDATE ON project_managed_secrets FOR EACH ROW EXECUTE FUNCTION nerocd_database_backup_secret_hold();
CREATE FUNCTION nerocd_database_backup_receipt() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE b hosting_database_backups; l run_leases; a jsonb:=NEW.authority;
BEGIN
 SELECT * INTO b FROM hosting_database_backups WHERE id=NEW.operation_id;
 SELECT * INTO l FROM run_leases WHERE id=a->>'lease_id';
 IF b.id IS NULL OR l.id IS NULL OR b.run_id IS DISTINCT FROM l.run_id OR b.runner_id IS DISTINCT FROM l.runner_id OR a->>'operation_id' IS DISTINCT FROM b.id OR a->>'run_id' IS DISTINCT FROM b.run_id OR a->>'attempt' IS DISTINCT FROM l.attempt::text OR a->>'fence' IS DISTINCT FROM l.fence OR NEW.body->>'receipt_id' IS DISTINCT FROM NEW.receipt_id OR NEW.body->>'plan_hash' IS DISTINCT FROM b.plan_hash OR NEW.body->>'phase' IS DISTINCT FROM NEW.phase OR NEW.body - ARRAY['receipt_id','plan_hash','phase','blob','failure_code'] IS DISTINCT FROM a OR l.expires_at<=clock_timestamp() OR NEW.response->>'id' IS DISTINCT FROM b.id OR NEW.response->>'state' IS DISTINCT FROM b.state OR NEW.response->'blob' IS DISTINCT FROM b.blob THEN RAISE EXCEPTION 'database backup receipt authority mismatch' USING ERRCODE='23514'; END IF;
 IF (NEW.phase='intent' AND (b.state<>'applying' OR l.status<>'active' OR NOT b.ownership_held)) OR (NEW.phase='complete' AND (b.state<>'succeeded' OR l.status<>'succeeded' OR b.ownership_held OR NEW.body->'blob' IS DISTINCT FROM b.blob)) OR (NEW.phase='failed' AND (b.state<>'failed' OR l.status<>'failed' OR b.ownership_held IS DISTINCT FROM (b.mode='restore'))) OR (NEW.phase='uncertain' AND (b.state<>'uncertain' OR l.status<>'failed' OR NOT b.ownership_held)) THEN RAISE EXCEPTION 'database backup receipt settlement mismatch' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER hosting_database_backup_receipt BEFORE INSERT ON hosting_database_backup_receipts FOR EACH ROW EXECUTE FUNCTION nerocd_database_backup_receipt();
CREATE FUNCTION nerocd_database_backup_lease() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE b hosting_database_backups; r runners; spec jsonb;
BEGIN
 SELECT run_spec INTO spec FROM task_runs WHERE id=NEW.run_id;
 IF spec->>'type' IS DISTINCT FROM 'database_backup' THEN RETURN NEW; END IF;
 SELECT * INTO b FROM hosting_database_backups WHERE run_id=NEW.run_id;
 SELECT * INTO r FROM runners WHERE id=NEW.runner_id;
 IF b.id IS NULL OR b.runner_id<>r.id OR NOT b.ownership_held OR b.state<>'dispatched' OR r.status<>'active' OR r.token_hash='' OR NOT('database_backup'=ANY(r.capabilities)) OR NOT('hosting_postgresql_backup_v1'=ANY(r.capabilities)) OR 'shell'=ANY(r.capabilities) OR spec<>jsonb_build_object('type','database_backup','inputs','{}'::jsonb,'hosting_operation_id',b.id,'hosting_mode',b.mode) OR EXISTS(SELECT 1 FROM run_leases WHERE run_id=NEW.run_id) THEN RAISE EXCEPTION 'database backup lease identity mismatch' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER hosting_database_backup_lease BEFORE INSERT ON run_leases FOR EACH ROW EXECUTE FUNCTION nerocd_database_backup_lease();
DO $$ DECLARE owner_schema text:=current_schema(); routine text; BEGIN
 FOREACH routine IN ARRAY ARRAY['nerocd_database_backup_admission()','nerocd_database_backup_identity()','nerocd_database_backup_receipt()','nerocd_database_backup_mutation_hold()','nerocd_database_backup_secret_hold()','nerocd_database_backup_lease()'] LOOP EXECUTE format('ALTER FUNCTION %I.%s SET search_path=%I,pg_catalog,pg_temp',owner_schema,routine,owner_schema); END LOOP;
END $$;
