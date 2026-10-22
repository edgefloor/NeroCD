-- Per-release ports and proxy references share destination-scoped transaction
-- admission. Migration 0045 and its immutable evidence remain unchanged.
CREATE TABLE hosting_release_slots (
 destination_id text NOT NULL REFERENCES destinations(id) ON DELETE RESTRICT,
 host_port integer NOT NULL CHECK(host_port BETWEEN 1024 AND 65535),
 binding_id text NOT NULL REFERENCES resource_bindings(id) ON DELETE RESTRICT,
 PRIMARY KEY(destination_id,host_port),
 UNIQUE(destination_id,host_port,binding_id)
);
CREATE TABLE hosting_release_claims (
 operation_id text PRIMARY KEY REFERENCES hosting_operations(id) ON DELETE RESTRICT,
 destination_id text NOT NULL REFERENCES destinations(id) ON DELETE RESTRICT,
 host_port integer NOT NULL CHECK(host_port BETWEEN 1024 AND 65535),
 binding_id text NOT NULL REFERENCES resource_bindings(id) ON DELETE RESTRICT,
 state text NOT NULL CHECK(state IN ('reserved','held','retained','released'))
);
CREATE TABLE hosting_retained_releases (
 operation_id text PRIMARY KEY REFERENCES hosting_operations(id) ON DELETE RESTRICT,
 binding_id text NOT NULL REFERENCES resource_bindings(id) ON DELETE RESTRICT,
 destination_id text NOT NULL REFERENCES destinations(id) ON DELETE RESTRICT,
 host_port integer NOT NULL CHECK(host_port BETWEEN 1024 AND 65535),
 body jsonb NOT NULL CHECK(jsonb_typeof(body)='object'),
 body_hash text NOT NULL CHECK(body_hash ~ '^sha256:[a-f0-9]{64}$'),
 state text NOT NULL CHECK(state IN ('healthy','retained','cleanup_pending','removed')),
 retained_running boolean NOT NULL,
 cleanup_operation_id text REFERENCES hosting_operations(id) ON DELETE RESTRICT
);
CREATE TABLE hosting_cleanup_targets (
 operation_id text PRIMARY KEY REFERENCES hosting_operations(id) ON DELETE RESTRICT,
 target_operation_id text NOT NULL REFERENCES hosting_retained_releases(operation_id) ON DELETE RESTRICT,
 plan jsonb NOT NULL CHECK(jsonb_typeof(plan)='object'),
 plan_hash text NOT NULL CHECK(plan_hash ~ '^sha256:[a-f0-9]{64}$')
);
ALTER TABLE hosting_task_targets DROP CONSTRAINT hosting_task_targets_mode_check;
ALTER TABLE hosting_task_targets ADD CONSTRAINT hosting_task_targets_mode_check CHECK(mode IN ('deploy','observe','cleanup'));
CREATE UNIQUE INDEX hosting_one_cleanup_task ON hosting_task_targets(operation_id) WHERE mode='cleanup';

-- Existing settled healthy targets reserve their actual ports; unresolved
-- attempts retain their existing mutation and restore holds.
INSERT INTO hosting_release_slots(destination_id,host_port,binding_id)
 SELECT destination_id,host_port,binding_id FROM hosting_binding_state;
INSERT INTO hosting_retained_releases(operation_id,binding_id,destination_id,host_port,body,body_hash,state,retained_running)
 SELECT healthy->>'operation_id',binding_id,destination_id,(healthy->'snapshot'->>'host_port')::integer,healthy,
 'sha256:'||encode(sha256(convert_to(healthy::text,'UTF8')),'hex'),'healthy',true
 FROM hosting_binding_state WHERE healthy IS NOT NULL;

CREATE TABLE hosting_proxies (
 id text PRIMARY KEY,
 server_id text NOT NULL UNIQUE REFERENCES servers(id) ON DELETE RESTRICT,
 destination_id text NOT NULL UNIQUE REFERENCES destinations(id) ON DELETE RESTRICT,
 runner_id text NOT NULL REFERENCES runners(id) ON DELETE RESTRICT,
 control_project_id text NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
 configuration jsonb NOT NULL CHECK(jsonb_typeof(configuration)='object'),
 enabled boolean NOT NULL DEFAULT true,
 desired_generation bigint NOT NULL DEFAULT 0 CHECK(desired_generation>=0),
 loaded_generation bigint NOT NULL DEFAULT 0 CHECK(loaded_generation>=0),
 daemon_id text,
 static_hash text,
 active_operation_id text,
 hold_operation_id text,
 created_by text NOT NULL REFERENCES principals(id) ON DELETE RESTRICT,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE hosting_proxy_hostname_grants (
 id text PRIMARY KEY,
 proxy_id text NOT NULL REFERENCES hosting_proxies(id) ON DELETE RESTRICT,
 project_id text NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
 hostname text NOT NULL CHECK(length(hostname) BETWEEN 3 AND 253),
 wildcard boolean NOT NULL,
 granted_by text NOT NULL REFERENCES principals(id) ON DELETE RESTRICT,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 revoked_at timestamptz,
 UNIQUE(proxy_id,project_id,hostname,wildcard)
);
CREATE TABLE hosting_proxy_routes (
 id text PRIMARY KEY,
 proxy_id text NOT NULL REFERENCES hosting_proxies(id) ON DELETE RESTRICT,
 project_id text NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
 resource_id text NOT NULL REFERENCES resources(id) ON DELETE RESTRICT,
 binding_id text NOT NULL REFERENCES resource_bindings(id) ON DELETE RESTRICT,
 grant_id text NOT NULL REFERENCES hosting_proxy_hostname_grants(id) ON DELETE RESTRICT,
 hostname text NOT NULL CHECK(length(hostname) BETWEEN 3 AND 253),
 path_prefix text NOT NULL CHECK(length(path_prefix) BETWEEN 1 AND 256),
 version bigint NOT NULL CHECK(version>=1),
 selected_release_operation_id text NOT NULL REFERENCES hosting_operations(id) ON DELETE RESTRICT,
 enabled boolean NOT NULL DEFAULT true,
 created_by text NOT NULL REFERENCES principals(id) ON DELETE RESTRICT,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(proxy_id,hostname,path_prefix)
);
CREATE TABLE hosting_proxy_operations (
 id text PRIMARY KEY,
 proxy_id text NOT NULL REFERENCES hosting_proxies(id) ON DELETE RESTRICT,
 project_id text REFERENCES projects(id) ON DELETE RESTRICT,
 route_id text REFERENCES hosting_proxy_routes(id) ON DELETE RESTRICT,
 generation bigint NOT NULL CHECK(generation>=1),
 idempotency_key text NOT NULL CHECK(length(idempotency_key) BETWEEN 1 AND 128),
 request_hash text NOT NULL CHECK(request_hash ~ '^sha256:[a-f0-9]{64}$'),
 state text NOT NULL CHECK(state IN ('queued','dispatched','prepared','applying','applied','rolled_back','refused','uncertain','superseded','canceled','cancel_requested')),
 snapshot jsonb NOT NULL CHECK(jsonb_typeof(snapshot)='object'),
 snapshot_hash text NOT NULL CHECK(snapshot_hash ~ '^sha256:[a-f0-9]{64}$'),
 plan jsonb CHECK(plan IS NULL OR jsonb_typeof(plan)='object'),
 plan_hash text,
 run_id text UNIQUE REFERENCES task_runs(id) ON DELETE RESTRICT,
 created_by text NOT NULL REFERENCES principals(id) ON DELETE RESTRICT,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(proxy_id,generation),
 UNIQUE(proxy_id,idempotency_key)
);
ALTER TABLE hosting_proxies ADD FOREIGN KEY(active_operation_id) REFERENCES hosting_proxy_operations(id) ON DELETE RESTRICT;
ALTER TABLE hosting_proxies ADD FOREIGN KEY(hold_operation_id) REFERENCES hosting_proxy_operations(id) ON DELETE RESTRICT;
CREATE TABLE hosting_proxy_task_targets (
 run_id text PRIMARY KEY REFERENCES task_runs(id) ON DELETE RESTRICT,
 operation_id text NOT NULL UNIQUE REFERENCES hosting_proxy_operations(id) ON DELETE RESTRICT,
 proxy_id text NOT NULL REFERENCES hosting_proxies(id) ON DELETE RESTRICT,
 server_id text NOT NULL REFERENCES servers(id) ON DELETE RESTRICT,
 destination_id text NOT NULL REFERENCES destinations(id) ON DELETE RESTRICT,
 runner_id text NOT NULL REFERENCES runners(id) ON DELETE RESTRICT,
 mode text NOT NULL CHECK(mode='apply')
);
CREATE TABLE hosting_proxy_admissions (
 operation_id text PRIMARY KEY REFERENCES hosting_proxy_operations(id) ON DELETE RESTRICT,
 run_id text NOT NULL REFERENCES hosting_proxy_task_targets(run_id) ON DELETE RESTRICT,
 lease_id text NOT NULL REFERENCES run_leases(id) ON DELETE RESTRICT,
 authority jsonb NOT NULL CHECK(jsonb_typeof(authority)='object'),
 plan_hash text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE hosting_proxy_receipts (
 operation_id text NOT NULL REFERENCES hosting_proxy_operations(id) ON DELETE RESTRICT,
 phase text NOT NULL CHECK(phase IN ('prepared','complete')),
 receipt_id text NOT NULL UNIQUE,
 authority jsonb NOT NULL CHECK(jsonb_typeof(authority)='object'),
 body jsonb NOT NULL CHECK(jsonb_typeof(body)='object'),
 body_hash text NOT NULL CHECK(body_hash ~ '^sha256:[a-f0-9]{64}$'),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(operation_id,phase)
);
CREATE TABLE hosting_proxy_restore_holds (
 proxy_id text PRIMARY KEY REFERENCES hosting_proxies(id) ON DELETE RESTRICT,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE hosting_proxy_release_references (
 operation_id text NOT NULL REFERENCES hosting_proxy_operations(id) ON DELETE RESTRICT,
 route_id text NOT NULL REFERENCES hosting_proxy_routes(id) ON DELETE RESTRICT,
 release_operation_id text NOT NULL REFERENCES hosting_operations(id) ON DELETE RESTRICT,
 PRIMARY KEY(operation_id,route_id)
);
CREATE INDEX hosting_proxy_release_references_target ON hosting_proxy_release_references(release_operation_id);
CREATE TRIGGER hosting_cleanup_targets_immutable BEFORE UPDATE OR DELETE ON hosting_cleanup_targets FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_execution_immutable();
CREATE TRIGGER hosting_proxy_task_targets_immutable BEFORE UPDATE OR DELETE ON hosting_proxy_task_targets FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_execution_immutable();
CREATE TRIGGER hosting_proxy_admissions_immutable BEFORE UPDATE OR DELETE ON hosting_proxy_admissions FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_execution_immutable();
CREATE TRIGGER hosting_proxy_receipts_immutable BEFORE UPDATE OR DELETE ON hosting_proxy_receipts FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_execution_immutable();
CREATE TRIGGER hosting_proxy_restore_holds_immutable BEFORE UPDATE OR DELETE ON hosting_proxy_restore_holds FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_execution_immutable();
CREATE TRIGGER hosting_proxy_release_references_immutable BEFORE UPDATE OR DELETE ON hosting_proxy_release_references FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_execution_immutable();
CREATE FUNCTION nerocd_hosting_proxy_schema_compatible() RETURNS boolean LANGUAGE sql STABLE AS $function$
 SELECT NOT EXISTS(SELECT 1 FROM (VALUES ('hosting_release_slots'),('hosting_release_claims'),('hosting_retained_releases'),('hosting_cleanup_targets'),('hosting_proxies'),('hosting_proxy_hostname_grants'),('hosting_proxy_routes'),('hosting_proxy_operations'),('hosting_proxy_task_targets'),('hosting_proxy_admissions'),('hosting_proxy_receipts'),('hosting_proxy_restore_holds'),('hosting_proxy_release_references')) t(name) WHERE to_regclass(current_schema()||'.'||t.name) IS NULL)
 AND NOT EXISTS(SELECT 1 FROM (VALUES ('hosting_cleanup_targets'),('hosting_proxy_task_targets'),('hosting_proxy_admissions'),('hosting_proxy_receipts'),('hosting_proxy_restore_holds'),('hosting_proxy_release_references')) t(name) WHERE NOT EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass(current_schema()||'.'||t.name) AND tgname=t.name||'_immutable' AND tgenabled<>'D'))
 AND NOT EXISTS(SELECT 1 FROM (VALUES ('hosting_retained_releases','hosting_release_identity'),('hosting_release_claims','hosting_release_claim_identity'),('hosting_release_slots','hosting_release_slots_immutable'),('hosting_proxies','hosting_proxy_identity'),('hosting_proxy_operations','hosting_proxy_operation_identity')) t(table_name,trigger_name) WHERE NOT EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass(current_schema()||'.'||t.table_name) AND tgname=t.trigger_name AND tgenabled<>'D'));
$function$;

-- Slots may be released by a verified targeted cleanup, but never reassigned by
-- an update. Historical claims retain their immutable original target.
CREATE TRIGGER hosting_release_slots_immutable BEFORE UPDATE ON hosting_release_slots FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_execution_immutable();
CREATE FUNCTION nerocd_hosting_release_claim_identity() RETURNS trigger LANGUAGE plpgsql AS $function$
BEGIN
 IF (NEW.operation_id,NEW.destination_id,NEW.host_port,NEW.binding_id) IS DISTINCT FROM (OLD.operation_id,OLD.destination_id,OLD.host_port,OLD.binding_id) THEN RAISE EXCEPTION 'release claim identity is immutable'; END IF;
 RETURN NEW;
END;
$function$;
CREATE TRIGGER hosting_release_claim_identity BEFORE UPDATE ON hosting_release_claims FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_release_claim_identity();

CREATE FUNCTION nerocd_hosting_release_identity() RETURNS trigger LANGUAGE plpgsql AS $function$
BEGIN
 IF (NEW.operation_id,NEW.binding_id,NEW.destination_id,NEW.host_port,NEW.body,NEW.body_hash) IS DISTINCT FROM (OLD.operation_id,OLD.binding_id,OLD.destination_id,OLD.host_port,OLD.body,OLD.body_hash) THEN RAISE EXCEPTION 'retained release identity is immutable'; END IF;
 RETURN NEW;
END;
$function$;
CREATE TRIGGER hosting_release_identity BEFORE UPDATE ON hosting_retained_releases FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_release_identity();
CREATE FUNCTION nerocd_hosting_proxy_identity() RETURNS trigger LANGUAGE plpgsql AS $function$
BEGIN
 IF (NEW.id,NEW.server_id,NEW.destination_id,NEW.runner_id,NEW.control_project_id,NEW.configuration,NEW.created_by,NEW.created_at) IS DISTINCT FROM (OLD.id,OLD.server_id,OLD.destination_id,OLD.runner_id,OLD.control_project_id,OLD.configuration,OLD.created_by,OLD.created_at)
 OR (OLD.daemon_id IS NOT NULL AND NEW.daemon_id IS DISTINCT FROM OLD.daemon_id)
 OR (OLD.static_hash IS NOT NULL AND NEW.static_hash IS DISTINCT FROM OLD.static_hash)
 OR NEW.desired_generation<OLD.desired_generation OR NEW.loaded_generation<OLD.loaded_generation THEN RAISE EXCEPTION 'proxy identity and committed generations are immutable'; END IF;
 RETURN NEW;
END;
$function$;
CREATE TRIGGER hosting_proxy_identity BEFORE UPDATE ON hosting_proxies FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_proxy_identity();
CREATE FUNCTION nerocd_hosting_proxy_operation_identity() RETURNS trigger LANGUAGE plpgsql AS $function$
BEGIN
 IF (NEW.id,NEW.proxy_id,NEW.project_id,NEW.route_id,NEW.generation,NEW.idempotency_key,NEW.request_hash,NEW.snapshot,NEW.snapshot_hash,NEW.created_by,NEW.created_at) IS DISTINCT FROM (OLD.id,OLD.proxy_id,OLD.project_id,OLD.route_id,OLD.generation,OLD.idempotency_key,OLD.request_hash,OLD.snapshot,OLD.snapshot_hash,OLD.created_by,OLD.created_at)
 OR (OLD.plan IS NOT NULL AND (NEW.plan,NEW.plan_hash) IS DISTINCT FROM (OLD.plan,OLD.plan_hash))
 OR (OLD.run_id IS NOT NULL AND NEW.run_id IS DISTINCT FROM OLD.run_id) THEN RAISE EXCEPTION 'proxy operation evidence is immutable'; END IF;
 RETURN NEW;
END;
$function$;
CREATE TRIGGER hosting_proxy_operation_identity BEFORE UPDATE ON hosting_proxy_operations FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_proxy_operation_identity();
