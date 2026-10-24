-- Read-only initialization-origin capture has its own task, never a deployment.
CREATE TABLE hosting_database_network_captures (
 id text PRIMARY KEY CHECK(id ~ '^hnc_[0-9a-f]{32}$'),
 source_operation_id text NOT NULL UNIQUE REFERENCES hosting_operations(id),
 resource_id text NOT NULL REFERENCES hosting_databases(resource_id),
 binding_id text NOT NULL REFERENCES resource_bindings(id),
 project_id text NOT NULL REFERENCES projects(id),
 run_id text NOT NULL UNIQUE REFERENCES task_runs(id),
 plan jsonb NOT NULL CHECK(jsonb_typeof(plan)='object' AND octet_length(plan::text)<=16384),
 plan_hash text NOT NULL CHECK(plan_hash ~ '^sha256:[0-9a-f]{64}$'),
 created_by text NOT NULL REFERENCES principals(id),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 CHECK((plan->>'version'='database-network.capture.v1' AND plan->>'operation_id'=id AND plan->>'source_operation_id'=source_operation_id AND plan->>'resource_id'=resource_id AND plan->>'binding_id'=binding_id AND plan->>'project_id'=project_id AND plan->>'hash'=plan_hash AND run_id='capture_'||id) IS TRUE)
);
CREATE TABLE hosting_database_network_origin_receipts (
 capture_id text PRIMARY KEY REFERENCES hosting_database_network_captures(id),
 receipt_id text NOT NULL UNIQUE,
 authority jsonb NOT NULL CHECK(jsonb_typeof(authority)='object' AND octet_length(authority::text)<=2048),
 body jsonb NOT NULL CHECK(jsonb_typeof(body)='object' AND octet_length(body::text)<=16384),
 body_hash text NOT NULL CHECK(body_hash ~ '^sha256:[0-9a-f]{64}$'),
 receipt jsonb NOT NULL CHECK(jsonb_typeof(receipt)='object' AND octet_length(receipt::text)<=8192),
 audit_id text NOT NULL UNIQUE REFERENCES audit_events(id) DEFERRABLE INITIALLY DEFERRED,
 CHECK((body @> authority AND body->>'operation_id'=capture_id AND body->>'receipt_id'=receipt_id AND receipt->>'operation_id'=capture_id AND receipt->>'id'=receipt_id AND receipt->>'body_hash'=body_hash) IS TRUE)
);
CREATE TRIGGER hosting_database_network_captures_immutable BEFORE UPDATE OR DELETE ON hosting_database_network_captures FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_runtime_immutable();
CREATE TRIGGER hosting_database_network_origin_receipts_immutable BEFORE UPDATE OR DELETE ON hosting_database_network_origin_receipts FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_runtime_immutable();

CREATE FUNCTION nerocd_database_network_capture_consistent() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE c hosting_database_network_captures; source hosting_operations; healthy jsonb; spec jsonb; materialization jsonb;
BEGIN
 IF TG_TABLE_NAME='hosting_database_network_captures' THEN SELECT * INTO STRICT c FROM hosting_database_network_captures WHERE id=NEW.id;
 ELSE SELECT * INTO STRICT c FROM hosting_database_network_captures WHERE id=NEW.capture_id; END IF;
 SELECT * INTO STRICT source FROM hosting_operations WHERE id=c.source_operation_id;
 SELECT s.healthy INTO STRICT healthy FROM hosting_binding_state s WHERE s.binding_id=c.binding_id;
 SELECT run_spec INTO STRICT spec FROM task_runs WHERE id=c.run_id;
 IF source.state<>'succeeded' OR source.resource_id<>c.resource_id OR source.binding_id<>c.binding_id OR source.project_id<>c.project_id OR healthy IS DISTINCT FROM c.plan->'source_healthy' OR c.plan->'source_plan' IS DISTINCT FROM (SELECT body FROM hosting_apply_plans WHERE operation_id=source.id) OR c.plan->'source_plan'->>'compiler_version'<>'database-hosting.private.owned-storage.v1' OR spec IS DISTINCT FROM jsonb_build_object('type','database_network_capture','hosting_operation_id',c.id,'hosting_mode','capture','inputs','{}'::jsonb) THEN RAISE EXCEPTION 'database network capture source mismatch' USING ERRCODE='23514'; END IF;
 IF EXISTS(SELECT 1 FROM hosting_operations WHERE binding_id=c.binding_id AND state NOT IN('succeeded','failed','canceled')) OR EXISTS(SELECT 1 FROM hosting_binding_state WHERE binding_id=c.binding_id AND (hold_operation_id IS NOT NULL OR NOT restore_admitted)) OR EXISTS(SELECT 1 FROM hosting_binding_restore_holds WHERE binding_id=c.binding_id) THEN RAISE EXCEPTION 'database network capture source held' USING ERRCODE='23514'; END IF;
 SELECT r.body INTO STRICT materialization FROM hosting_storage_materializations m JOIN hosting_storage_materialized_receipts r ON r.operation_id=m.materialized_operation_id AND r.receipt_id=m.materialized_receipt_id WHERE m.storage_id=c.plan->'storage'->>'id';
 IF c.plan->'creation'->>'receipt' IS NULL OR c.plan->'creation'->'receipt'->>'body_hash' IS DISTINCT FROM (SELECT r.body_hash FROM hosting_storage_materializations m JOIN hosting_storage_materialized_receipts r ON r.operation_id=m.materialized_operation_id AND r.receipt_id=m.materialized_receipt_id WHERE m.storage_id=c.plan->'storage'->>'id') OR c.plan->'creation'->'labels' IS DISTINCT FROM materialization->'labels' THEN RAISE EXCEPTION 'database network capture creation mismatch' USING ERRCODE='23514'; END IF;
 IF TG_TABLE_NAME='hosting_database_network_origin_receipts' THEN
 IF ((NEW.body->>'plan_hash') IS DISTINCT FROM c.plan_hash OR NEW.body->'origin'->>'init_ready' IS DISTINCT FROM 'true' OR NEW.body->'origin'->>'binding' IS DISTINCT FROM c.binding_id OR NEW.body->'origin'->>'destination' IS DISTINCT FROM c.plan->>'destination_id' OR NEW.body->'origin'->>'daemon' IS DISTINCT FROM c.plan->'materialization'->>'daemon_id' OR NEW.body->'origin'->>'configuration' IS DISTINCT FROM c.plan->'source_plan'->'snapshot'->'database'->>'configuration_hash' OR NEW.body->'origin'->'storage' IS DISTINCT FROM c.plan->'materialization'->'storage_ref' OR NEW.body->'origin'->>'origin_operation' IS DISTINCT FROM c.plan->'materialization'->>'creation_operation_id' OR NEW.body->'origin'->>'origin_plan' IS DISTINCT FROM c.plan->'materialization'->>'creation_plan_hash' OR NEW.body->'origin'->>'volume_nonce' IS DISTINCT FROM c.plan->'materialization'->>'creation_nonce') THEN RAISE EXCEPTION 'database network capture origin mismatch' USING ERRCODE='23514'; END IF;
 END IF;
 RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER hosting_database_network_capture_consistency AFTER INSERT ON hosting_database_network_captures DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION nerocd_database_network_capture_consistent();
CREATE CONSTRAINT TRIGGER hosting_database_network_origin_consistency AFTER INSERT ON hosting_database_network_origin_receipts DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION nerocd_database_network_capture_consistent();

CREATE FUNCTION nerocd_database_network_capture_lease() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE c hosting_database_network_captures;
BEGIN
 SELECT * INTO c FROM hosting_database_network_captures WHERE run_id=NEW.run_id;
 IF NOT FOUND THEN RETURN NEW; END IF;
 IF NEW.runner_id IS DISTINCT FROM c.plan->>'runner_id' OR EXISTS(SELECT 1 FROM hosting_database_network_origin_receipts WHERE capture_id=c.id) OR NOT EXISTS(SELECT 1 FROM runners WHERE id=NEW.runner_id AND status='active' AND token_hash<>'' AND 'hosting_database_network_capture_v1'=ANY(capabilities) AND NOT('shell'=ANY(capabilities))) OR NOT EXISTS(SELECT 1 FROM runner_enrollments e JOIN runners r ON r.id=e.runner_id WHERE e.runner_id=NEW.runner_id AND e.used_at IS NOT NULL AND e.revoked_at IS NULL AND e.credential_hash=r.token_hash) THEN RAISE EXCEPTION 'database network capture runner denied' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER hosting_database_network_capture_lease BEFORE INSERT ON run_leases FOR EACH ROW EXECUTE FUNCTION nerocd_database_network_capture_lease();
CREATE FUNCTION nerocd_database_network_capture_flush() RETURNS void LANGUAGE plpgsql AS $$ BEGIN SET CONSTRAINTS ALL IMMEDIATE; END $$;
CREATE FUNCTION nerocd_database_network_capture_schema_compatible() RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT NOT EXISTS(SELECT 1 FROM (VALUES('hosting_database_network_captures','hosting_database_network_captures_immutable'),('hosting_database_network_origin_receipts','hosting_database_network_origin_receipts_immutable'),('hosting_database_network_captures','hosting_database_network_capture_consistency'),('hosting_database_network_origin_receipts','hosting_database_network_origin_consistency'),('run_leases','hosting_database_network_capture_lease')) required(table_name,trigger_name) WHERE NOT EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass(current_schema()||'.'||table_name) AND tgname=trigger_name AND tgenabled IN('O','A')));
$$;
DO $$ DECLARE owner_schema text:=current_schema(); routine text; BEGIN
 FOREACH routine IN ARRAY ARRAY['nerocd_database_network_capture_consistent()','nerocd_database_network_capture_lease()','nerocd_database_network_capture_flush()','nerocd_database_network_capture_schema_compatible()'] LOOP EXECUTE format('ALTER FUNCTION %I.%s SET search_path=%I,pg_catalog,pg_temp',owner_schema,routine,owner_schema); END LOOP;
END $$;
