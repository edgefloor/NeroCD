-- Image operations keep external mutation authority independent of task status.
CREATE TABLE hosting_operations (
 id text PRIMARY KEY,
 project_id text NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
 resource_id text NOT NULL REFERENCES resources(id) ON DELETE RESTRICT,
 binding_id text NOT NULL REFERENCES resource_bindings(id) ON DELETE RESTRICT,
 idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 128),
 request_hash text NOT NULL CHECK (request_hash ~ '^sha256:[a-f0-9]{64}$'),
 state text NOT NULL CHECK (state IN ('queued','dispatched','preparing','prepared','applying','verifying','succeeded','failed','canceled','cancel_requested','reconciliation_required')),
 run_id text UNIQUE REFERENCES task_runs(id) ON DELETE RESTRICT,
 reconciliation_run_id text UNIQUE REFERENCES task_runs(id) ON DELETE RESTRICT,
 reconcile_requested boolean NOT NULL DEFAULT false,
 created_by text NOT NULL REFERENCES principals(id) ON DELETE RESTRICT,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(resource_id,idempotency_key)
);
CREATE TABLE hosting_snapshots (
 operation_id text PRIMARY KEY REFERENCES hosting_operations(id) ON DELETE RESTRICT,
 body jsonb NOT NULL CHECK(jsonb_typeof(body)='object'),
 body_hash text NOT NULL CHECK(body_hash ~ '^sha256:[a-f0-9]{64}$')
);
CREATE TABLE hosting_releases (
 operation_id text PRIMARY KEY REFERENCES hosting_operations(id) ON DELETE RESTRICT,
 body jsonb NOT NULL CHECK(jsonb_typeof(body)='object'),
 body_hash text NOT NULL CHECK(body_hash ~ '^sha256:[a-f0-9]{64}$')
);
CREATE TABLE hosting_apply_plans (
 operation_id text PRIMARY KEY REFERENCES hosting_operations(id) ON DELETE RESTRICT,
 body jsonb NOT NULL CHECK(jsonb_typeof(body)='object'),
 body_hash text NOT NULL CHECK(body_hash ~ '^sha256:[a-f0-9]{64}$')
);
CREATE TABLE hosting_task_targets (
 run_id text PRIMARY KEY REFERENCES task_runs(id) ON DELETE RESTRICT,
 operation_id text NOT NULL REFERENCES hosting_operations(id) ON DELETE RESTRICT,
 mode text NOT NULL CHECK(mode IN ('deploy','observe')),
 binding_id text NOT NULL REFERENCES resource_bindings(id) ON DELETE RESTRICT,
 runner_id text NOT NULL REFERENCES runners(id) ON DELETE RESTRICT
);
CREATE UNIQUE INDEX hosting_one_deployment_task ON hosting_task_targets(operation_id) WHERE mode='deploy';
CREATE UNIQUE INDEX hosting_one_observation_task ON hosting_task_targets(operation_id) WHERE mode='observe';
CREATE TABLE hosting_apply_admissions (
 operation_id text PRIMARY KEY REFERENCES hosting_operations(id) ON DELETE RESTRICT,
 run_id text NOT NULL REFERENCES hosting_task_targets(run_id) ON DELETE RESTRICT,
 lease_id text NOT NULL REFERENCES run_leases(id) ON DELETE RESTRICT,
 authority jsonb NOT NULL,
 plan_hash text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE hosting_reconciliation_receipts (
 lease_id text PRIMARY KEY REFERENCES run_leases(id) ON DELETE RESTRICT,
 operation_id text NOT NULL REFERENCES hosting_operations(id) ON DELETE RESTRICT,
 body jsonb NOT NULL,
 body_hash text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE hosting_dispatch_receipts (
 operation_id text PRIMARY KEY REFERENCES hosting_operations(id) ON DELETE RESTRICT,
 run_id text NOT NULL UNIQUE REFERENCES hosting_task_targets(run_id) ON DELETE RESTRICT,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE hosting_receipts (
 operation_id text NOT NULL REFERENCES hosting_operations(id) ON DELETE RESTRICT,
 phase text NOT NULL CHECK(phase IN ('prepared','complete')),
 receipt_id text NOT NULL UNIQUE,
 authority jsonb NOT NULL CHECK(jsonb_typeof(authority)='object'),
 body jsonb NOT NULL CHECK(jsonb_typeof(body)='object'),
 body_hash text NOT NULL CHECK(body_hash ~ '^sha256:[a-f0-9]{64}$'),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(operation_id,phase)
);
CREATE TABLE hosting_binding_state (
 binding_id text PRIMARY KEY REFERENCES resource_bindings(id) ON DELETE RESTRICT,
 destination_id text NOT NULL REFERENCES destinations(id) ON DELETE RESTRICT,
 host_port integer NOT NULL CHECK(host_port BETWEEN 1024 AND 65535),
 daemon_id text CHECK(length(daemon_id) BETWEEN 1 AND 128),
 healthy jsonb CHECK(healthy IS NULL OR jsonb_typeof(healthy)='object'),
 hold_operation_id text UNIQUE REFERENCES hosting_operations(id) ON DELETE RESTRICT,
 restore_admitted boolean NOT NULL DEFAULT true,
 UNIQUE(destination_id,host_port)
);

CREATE FUNCTION nerocd_hosting_execution_immutable() RETURNS trigger LANGUAGE plpgsql AS $function$
BEGIN
 RAISE EXCEPTION 'hosting evidence is immutable';
END;
$function$;
CREATE TRIGGER hosting_apply_admissions_immutable BEFORE UPDATE OR DELETE ON hosting_apply_admissions FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_execution_immutable();
CREATE TRIGGER hosting_reconciliation_receipts_immutable BEFORE UPDATE OR DELETE ON hosting_reconciliation_receipts FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_execution_immutable();
CREATE TRIGGER hosting_snapshots_immutable BEFORE UPDATE OR DELETE ON hosting_snapshots FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_execution_immutable();
CREATE TRIGGER hosting_releases_immutable BEFORE UPDATE OR DELETE ON hosting_releases FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_execution_immutable();
CREATE TRIGGER hosting_apply_plans_immutable BEFORE UPDATE OR DELETE ON hosting_apply_plans FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_execution_immutable();
CREATE TRIGGER hosting_task_targets_immutable BEFORE UPDATE OR DELETE ON hosting_task_targets FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_execution_immutable();
CREATE TRIGGER hosting_dispatch_receipts_immutable BEFORE UPDATE OR DELETE ON hosting_dispatch_receipts FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_execution_immutable();
CREATE TRIGGER hosting_receipts_immutable BEFORE UPDATE OR DELETE ON hosting_receipts FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_execution_immutable();

CREATE FUNCTION nerocd_hosting_operation_identity() RETURNS trigger LANGUAGE plpgsql AS $function$
BEGIN
 IF (NEW.id,NEW.project_id,NEW.resource_id,NEW.binding_id,NEW.idempotency_key,NEW.request_hash,NEW.created_by,NEW.created_at)
 IS DISTINCT FROM (OLD.id,OLD.project_id,OLD.resource_id,OLD.binding_id,OLD.idempotency_key,OLD.request_hash,OLD.created_by,OLD.created_at)
 OR (OLD.run_id IS NOT NULL AND NEW.run_id IS DISTINCT FROM OLD.run_id) THEN
  RAISE EXCEPTION 'hosting operation identity is immutable';
 END IF;
 RETURN NEW;
END;
$function$;
CREATE TRIGGER hosting_operation_identity BEFORE UPDATE ON hosting_operations FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_operation_identity();

CREATE FUNCTION nerocd_hosting_binding_identity() RETURNS trigger LANGUAGE plpgsql AS $function$
BEGIN
 IF (NEW.binding_id,NEW.destination_id,NEW.host_port) IS DISTINCT FROM (OLD.binding_id,OLD.destination_id,OLD.host_port)
 OR (OLD.daemon_id IS NOT NULL AND NEW.daemon_id IS DISTINCT FROM OLD.daemon_id) THEN
  RAISE EXCEPTION 'hosting target, reservation, and observed daemon are immutable';
 END IF;
 RETURN NEW;
END;
$function$;
CREATE TRIGGER hosting_binding_identity BEFORE UPDATE ON hosting_binding_state FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_binding_identity();

CREATE FUNCTION nerocd_hosting_execution_schema_compatible() RETURNS boolean LANGUAGE sql STABLE AS $function$
SELECT NOT EXISTS (
 SELECT 1 FROM (VALUES ('hosting_operations'),('hosting_snapshots'),('hosting_releases'),('hosting_apply_plans'),('hosting_task_targets'),('hosting_dispatch_receipts'),('hosting_receipts'),('hosting_binding_state'),('hosting_apply_admissions'),('hosting_reconciliation_receipts')) t(name)
 WHERE to_regclass(current_schema()||'.'||t.name) IS NULL
) AND NOT EXISTS (
 SELECT 1 FROM (VALUES ('hosting_snapshots'),('hosting_releases'),('hosting_apply_plans'),('hosting_task_targets'),('hosting_dispatch_receipts'),('hosting_receipts'),('hosting_apply_admissions'),('hosting_reconciliation_receipts')) t(name)
 WHERE NOT EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass(current_schema()||'.'||t.name) AND tgname=t.name||'_immutable' AND tgenabled<>'D')
) AND EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid='hosting_binding_state'::regclass AND tgname='hosting_binding_identity' AND tgenabled<>'D')
AND EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid='hosting_operations'::regclass AND tgname='hosting_operation_identity' AND tgenabled<>'D');
$function$;
