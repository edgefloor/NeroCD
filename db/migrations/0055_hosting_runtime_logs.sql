-- Opt-in runtime tails are separate from task logs, leases and attempt journals.
CREATE TABLE hosting_runtime_log_sessions (
 id text PRIMARY KEY CHECK(octet_length(id) BETWEEN 1 AND 128),
 actor_id text NOT NULL REFERENCES principals(id),
 resource_id text NOT NULL REFERENCES resources(id) ON DELETE CASCADE,
 project_id text NOT NULL REFERENCES projects(id),
 runner_id text NOT NULL REFERENCES runners(id),
 request_id text NOT NULL CHECK(request_id ~ '^[0-9a-f]{32}$'),
 request_hash text NOT NULL CHECK(request_hash ~ '^sha256:[0-9a-f]{64}$'),
 request_expires_at timestamptz NOT NULL,
 target jsonb NOT NULL CHECK(jsonb_typeof(target)='object' AND octet_length(target::text)<=8192),
 authority_hash text NOT NULL CHECK(authority_hash ~ '^sha256:[0-9a-f]{64}$'),
 credential_hash text NOT NULL CHECK(credential_hash ~ '^[0-9a-f]{64}$'),
 state text NOT NULL DEFAULT 'waiting' CHECK(state IN('waiting','following','disconnected','ended','target_changed','authority_unavailable','unsupported_driver')),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 demand_expires_at timestamptz NOT NULL,
 ended_at timestamptz,
 purge_after timestamptz NOT NULL,
 instance_id text NOT NULL DEFAULT '' CHECK(instance_id='' OR instance_id ~ '^[0-9a-f]{32}$'),
 collection_epoch text NOT NULL DEFAULT '' CHECK(collection_epoch='' OR collection_epoch ~ '^[0-9a-f]{32}$'),
 epoch_expires_at timestamptz,
 last_batch_sequence integer NOT NULL DEFAULT 0 CHECK(last_batch_sequence>=0),
 last_sequence integer NOT NULL DEFAULT 0 CHECK(last_sequence>=0),
 deleted_through_sequence integer NOT NULL DEFAULT 0 CHECK(deleted_through_sequence>=0 AND deleted_through_sequence<=last_sequence),
 source_gap boolean NOT NULL DEFAULT false,
 retained_bytes integer NOT NULL DEFAULT 0 CHECK(retained_bytes BETWEEN 0 AND 1048576),
 retained_rows integer NOT NULL DEFAULT 0 CHECK(retained_rows BETWEEN 0 AND 1000),
 receipt jsonb CHECK(receipt IS NULL OR (jsonb_typeof(receipt)='object' AND octet_length(receipt::text)<=8192)),
 UNIQUE(actor_id,resource_id,request_id),
 CHECK(purge_after<=LEAST(demand_expires_at,COALESCE(ended_at,demand_expires_at))+interval '5 minutes')
);
CREATE INDEX hosting_runtime_log_purge ON hosting_runtime_log_sessions(purge_after,id);
CREATE INDEX hosting_runtime_log_actor_quota ON hosting_runtime_log_sessions(project_id,actor_id,demand_expires_at);
CREATE INDEX hosting_runtime_log_runner_collectors ON hosting_runtime_log_sessions(runner_id,epoch_expires_at);
CREATE TABLE hosting_runtime_log_events (
 session_id text NOT NULL REFERENCES hosting_runtime_log_sessions(id) ON DELETE CASCADE,
 sequence integer NOT NULL CHECK(sequence>0),
 component text NOT NULL CHECK(component='application'),
 stream text NOT NULL CHECK(stream IN('stdout','stderr','combined')),
 message text NOT NULL CHECK(octet_length(message)<=16384),
 source_timestamp timestamptz,
 message_truncated boolean NOT NULL,
 received_at timestamptz NOT NULL,
 PRIMARY KEY(session_id,sequence)
);
CREATE FUNCTION nerocd_runtime_log_identity() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF (NEW.id,NEW.actor_id,NEW.resource_id,NEW.project_id,NEW.runner_id,NEW.request_id,NEW.request_hash,NEW.request_expires_at,NEW.target,NEW.authority_hash,NEW.credential_hash,NEW.created_at)
 IS DISTINCT FROM (OLD.id,OLD.actor_id,OLD.resource_id,OLD.project_id,OLD.runner_id,OLD.request_id,OLD.request_hash,OLD.request_expires_at,OLD.target,OLD.authority_hash,OLD.credential_hash,OLD.created_at)
 OR NEW.last_batch_sequence<OLD.last_batch_sequence OR NEW.last_sequence<OLD.last_sequence OR NEW.deleted_through_sequence<OLD.deleted_through_sequence
 OR (OLD.ended_at IS NOT NULL AND (NEW.ended_at IS DISTINCT FROM OLD.ended_at OR NEW.demand_expires_at>OLD.demand_expires_at)) THEN
  RAISE EXCEPTION 'runtime log identity or terminal state changed' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END;$$;
CREATE TRIGGER hosting_runtime_log_identity BEFORE UPDATE ON hosting_runtime_log_sessions FOR EACH ROW EXECUTE FUNCTION nerocd_runtime_log_identity();
CREATE FUNCTION nerocd_hosting_runtime_log_schema_compatible() RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT to_regclass(current_schema()||'.hosting_runtime_log_sessions') IS NOT NULL
 AND to_regclass(current_schema()||'.hosting_runtime_log_events') IS NOT NULL
 AND EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass(current_schema()||'.hosting_runtime_log_sessions') AND tgname='hosting_runtime_log_identity' AND tgenabled IN('O','A'))
 AND EXISTS(SELECT 1 FROM pg_index WHERE indrelid=to_regclass(current_schema()||'.hosting_runtime_log_sessions') AND indexrelid=to_regclass(current_schema()||'.hosting_runtime_log_purge') AND indisvalid)
 AND EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid=to_regclass(current_schema()||'.hosting_runtime_log_events') AND contype='f' AND confdeltype='c' AND convalidated);
$$;
DO $migration$
BEGIN
 EXECUTE format('ALTER FUNCTION %I.nerocd_runtime_log_identity() SET search_path TO pg_catalog, %I, pg_temp',current_schema(),current_schema());
 EXECUTE format('ALTER FUNCTION %I.nerocd_hosting_runtime_log_schema_compatible() SET search_path TO %I, pg_catalog, pg_temp',current_schema(),current_schema());
END;$migration$;
