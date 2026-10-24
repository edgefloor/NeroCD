-- Connection records contain references and immutable nonsecret evidence only.
-- This first adapter supports one connected application family per database.
CREATE TABLE hosting_database_connections (
 id text PRIMARY KEY CHECK(id ~ '^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$'),
 application_resource_id text NOT NULL UNIQUE REFERENCES resources(id),
 database_resource_id text NOT NULL UNIQUE REFERENCES resources(id),
 project_id text NOT NULL REFERENCES projects(id),
 binding_id text NOT NULL UNIQUE REFERENCES hosting_database_bindings(id) DEFERRABLE INITIALLY DEFERRED,
 source_operation_id text NOT NULL REFERENCES hosting_operations(id),
 capture_id text NOT NULL,
 upgrade_operation_id text NOT NULL UNIQUE,
 application_operation_id text NOT NULL UNIQUE,
 accepted_request jsonb NOT NULL CHECK(jsonb_typeof(accepted_request)='object' AND octet_length(accepted_request::text)<=32768 AND accepted_request - 'binding' - 'deployment'='{}'::jsonb),
 request_hash text NOT NULL CHECK(request_hash ~ '^sha256:[0-9a-f]{64}$'),
 binding_application_snapshot jsonb NOT NULL CHECK(jsonb_typeof(binding_application_snapshot)='object' AND octet_length(binding_application_snapshot::text)<=16384),
 state text NOT NULL CHECK(state IN('binding_saved','capture_requested','capture_waiting','upgrade_requested','upgrade_waiting','application_requested','active','held')),
 reason_code text NOT NULL CHECK(reason_code IN('accepted','capture_required','capture_waiting','upgrade_waiting','application_waiting','active','authority_lost','child_failed','uncertainty_held')),
 created_by text NOT NULL REFERENCES principals(id),
 audit_id text NOT NULL UNIQUE REFERENCES audit_events(id) DEFERRABLE INITIALLY DEFERRED,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 CHECK(application_resource_id<>database_resource_id AND upgrade_operation_id<>application_operation_id AND upgrade_operation_id<>source_operation_id AND application_operation_id<>source_operation_id)
);

CREATE TABLE hosting_database_membership_heads (
 database_binding_id text PRIMARY KEY REFERENCES resource_bindings(id),
 connection_id text NOT NULL UNIQUE REFERENCES hosting_database_connections(id),
 origin_receipt_id text NOT NULL REFERENCES hosting_database_network_origin_receipts(receipt_id),
 revision bigint NOT NULL CHECK(revision>0),
 members jsonb NOT NULL CHECK(jsonb_typeof(members)='array' AND jsonb_array_length(members)<=1 AND octet_length(members::text)<=65536),
 retired jsonb NOT NULL CHECK(jsonb_typeof(retired)='array' AND jsonb_array_length(retired)<=64 AND octet_length(retired::text)<=131072),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE TABLE hosting_database_connection_executions (
 operation_id text PRIMARY KEY REFERENCES hosting_operations(id) DEFERRABLE INITIALLY DEFERRED,
 connection_id text NOT NULL REFERENCES hosting_database_connections(id),
 family text NOT NULL CHECK(family IN('application','maintenance')),
 expected_membership_revision bigint NOT NULL CHECK(expected_membership_revision>0),
 proof jsonb NOT NULL CHECK(jsonb_typeof(proof)='object' AND octet_length(proof::text)<=131072),
 proof_hash text NOT NULL CHECK(proof_hash ~ '^sha256:[0-9a-f]{64}$'),
 audit_id text NOT NULL UNIQUE REFERENCES audit_events(id) DEFERRABLE INITIALLY DEFERRED,
 CHECK((proof->>'hash'=proof_hash AND proof->>'membership_revision'=expected_membership_revision::text AND ((family='application' AND proof->>'version'='application-database.execution.v1' AND proof->>'operation_id'=operation_id) OR (family='maintenance' AND proof->>'version'='database-network.maintenance.v1'))) IS TRUE)
);

CREATE TABLE hosting_database_network_holds (
 database_binding_id text PRIMARY KEY REFERENCES resource_bindings(id),
 connection_id text NOT NULL REFERENCES hosting_database_connections(id),
 -- Workflow child IDs are reserved before their operation is admitted. The
 -- hold validator below permits only that connection's exact reserved child or
 -- an associated execution; the reservation itself grants no execution rights.
 operation_id text,
 acquired_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE TABLE hosting_application_database_stages (
 operation_id text NOT NULL REFERENCES hosting_database_connection_executions(operation_id),
 receipt_id text NOT NULL UNIQUE,
 phase text NOT NULL CHECK(phase IN('candidate_created','attach_intent','network_configured','attached','ready','uncertain')),
 previous_hash text,
 authority jsonb NOT NULL CHECK(jsonb_typeof(authority)='object' AND octet_length(authority::text)<=1024 AND authority - 'operation_id' - 'run_id' - 'lease_id' - 'attempt' - 'fence'='{}'::jsonb),
 stage jsonb NOT NULL CHECK(jsonb_typeof(stage)='object' AND octet_length(stage::text)<=16384 AND stage - 'version' - 'receipt_id' - 'operation_id' - 'plan_hash' - 'phase' - 'candidate' - 'attach_plan' - 'endpoint_receipt' - 'failure_code' - 'hash'='{}'::jsonb),
 stage_hash text NOT NULL CHECK(stage_hash ~ '^sha256:[0-9a-f]{64}$'),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(operation_id,phase),
 UNIQUE(operation_id,stage_hash),
 FOREIGN KEY(operation_id,previous_hash) REFERENCES hosting_application_database_stages(operation_id,stage_hash),
 CHECK((stage->>'version'='application-database.stage.v1' AND stage->>'receipt_id'=receipt_id AND stage->>'operation_id'=operation_id AND stage->>'phase'=phase AND stage->>'hash'=stage_hash AND authority->>'operation_id'=operation_id) IS TRUE)
);

CREATE TABLE hosting_application_database_retirements (
 operation_id text NOT NULL REFERENCES hosting_database_connection_executions(operation_id),
 phase text NOT NULL CHECK(phase IN('retire_intent','detached')),
 plan jsonb NOT NULL CHECK(jsonb_typeof(plan)='object' AND octet_length(plan::text)<=4096),
 receipt jsonb NOT NULL CHECK(jsonb_typeof(receipt)='object' AND octet_length(receipt::text)<=16384),
 receipt_id text NOT NULL UNIQUE,
 receipt_hash text NOT NULL CHECK(receipt_hash ~ '^sha256:[0-9a-f]{64}$'),
 authority jsonb NOT NULL CHECK(jsonb_typeof(authority)='object' AND octet_length(authority::text)<=1024),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(operation_id,phase),
 CHECK((plan->>'operation_id'=operation_id AND plan->>'action'='retire' AND receipt->>'operation_id'=operation_id AND receipt->>'phase'=phase AND receipt->>'receipt_id'=receipt_id AND receipt->>'hash'=receipt_hash AND receipt->>'plan_hash'=plan->>'hash' AND authority->>'operation_id'=operation_id) IS TRUE)
);

CREATE TABLE hosting_database_membership_receipts (
 database_binding_id text NOT NULL REFERENCES hosting_database_membership_heads(database_binding_id),
 revision bigint NOT NULL CHECK(revision>1),
 operation_id text NOT NULL REFERENCES hosting_database_connection_executions(operation_id),
 action text NOT NULL CHECK(action IN('attach','retire')),
 body jsonb NOT NULL CHECK(jsonb_typeof(body)='object' AND octet_length(body::text)<=131072),
 body_hash text NOT NULL CHECK(body_hash ~ '^sha256:[0-9a-f]{64}$'),
 PRIMARY KEY(database_binding_id,revision),
 UNIQUE(operation_id,action)
);

CREATE TABLE hosting_application_database_effect_authorizations (
 authorization_id text PRIMARY KEY CHECK(authorization_id ~ '^[0-9a-f]{32}$'),
 operation_id text NOT NULL REFERENCES hosting_database_connection_executions(operation_id),
 request jsonb NOT NULL CHECK(jsonb_typeof(request)='object' AND octet_length(request::text)<=2048),
 request_hash text NOT NULL CHECK(request_hash ~ '^sha256:[0-9a-f]{64}$'),
 response jsonb NOT NULL CHECK(jsonb_typeof(response)='object' AND octet_length(response::text)<=2048),
 use_until timestamptz NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 CHECK((request->>'authorization_id'=authorization_id AND request->>'operation_id'=operation_id AND response->>'authorization_id'=authorization_id AND response->>'operation_id'=operation_id AND response->>'action'=request->>'action' AND response->>'stage_hash'=request->>'expected_stage_hash' AND response->>'lease_id'=request->>'lease_id' AND response->>'fence'=request->>'fence' AND response->>'attempt'=request->>'attempt') IS TRUE)
);

CREATE TABLE hosting_database_connection_recoveries (
 id text PRIMARY KEY,
 connection_id text NOT NULL REFERENCES hosting_database_connections(id),
 operation_id text NOT NULL REFERENCES hosting_database_connection_executions(operation_id),
 actor_id text NOT NULL REFERENCES principals(id),
 idempotency_key text NOT NULL,
 accepted_request jsonb NOT NULL CHECK(jsonb_typeof(accepted_request)='object' AND octet_length(accepted_request::text)<=32768 AND accepted_request - 'action' - 'operation_id' - 'stage_hash' - 'candidate_hash' - 'expected_membership_revision' - 'idempotency_key'='{}'::jsonb),
 request_hash text NOT NULL CHECK(request_hash ~ '^sha256:[0-9a-f]{64}$'),
 audit_id text NOT NULL UNIQUE REFERENCES audit_events(id) DEFERRABLE INITIALLY DEFERRED,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(connection_id,idempotency_key),
 CHECK((accepted_request->>'action'='observe' AND accepted_request->>'operation_id'=operation_id AND accepted_request->>'idempotency_key'=idempotency_key) IS TRUE)
);

CREATE TRIGGER hosting_connection_executions_immutable BEFORE UPDATE OR DELETE ON hosting_database_connection_executions FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_runtime_immutable();
CREATE TRIGGER hosting_connection_stages_immutable BEFORE UPDATE OR DELETE ON hosting_application_database_stages FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_runtime_immutable();
CREATE TRIGGER hosting_connection_retirements_immutable BEFORE UPDATE OR DELETE ON hosting_application_database_retirements FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_runtime_immutable();
CREATE TRIGGER hosting_connection_membership_receipts_immutable BEFORE UPDATE OR DELETE ON hosting_database_membership_receipts FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_runtime_immutable();
CREATE TRIGGER hosting_connection_effect_authorizations_immutable BEFORE UPDATE OR DELETE ON hosting_application_database_effect_authorizations FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_runtime_immutable();
CREATE TRIGGER hosting_connection_recoveries_immutable BEFORE UPDATE OR DELETE ON hosting_database_connection_recoveries FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_runtime_immutable();

CREATE FUNCTION nerocd_database_connection_identity() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' OR (to_jsonb(NEW)-'state'-'reason_code'-'updated_at') IS DISTINCT FROM (to_jsonb(OLD)-'state'-'reason_code'-'updated_at') THEN RAISE EXCEPTION 'database connection identity is immutable' USING ERRCODE='23514'; END IF;
 IF OLD.state IN('active','held') AND NEW.state<>OLD.state THEN RAISE EXCEPTION 'database connection workflow is terminal' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;

CREATE FUNCTION nerocd_database_connection_hold_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE c hosting_database_connections; binding_key text; operation_key text; h hosting_database_network_holds;
BEGIN
 IF TG_TABLE_NAME='hosting_database_bindings' THEN
  SELECT * INTO c FROM hosting_database_connections WHERE database_resource_id=NEW.database_resource_id;
  IF FOUND AND c.application_resource_id<>NEW.application_resource_id THEN RAISE EXCEPTION 'database supports one connected application family' USING ERRCODE='23514'; END IF;
  RETURN NEW;
 ELSIF TG_TABLE_NAME='hosting_database_network_holds' THEN
  SELECT * INTO STRICT c FROM hosting_database_connections WHERE id=NEW.connection_id;
  SELECT b.database_binding_id INTO binding_key FROM hosting_database_bindings b WHERE b.id=c.binding_id;
  IF NEW.database_binding_id IS DISTINCT FROM binding_key OR NEW.operation_id IS NULL OR NOT (NEW.operation_id IN(c.upgrade_operation_id,c.application_operation_id) OR EXISTS(SELECT 1 FROM hosting_database_connection_executions e WHERE e.operation_id=NEW.operation_id AND e.connection_id=c.id)) OR (TG_OP='UPDATE' AND (NEW.connection_id,NEW.database_binding_id,NEW.acquired_at) IS DISTINCT FROM (OLD.connection_id,OLD.database_binding_id,OLD.acquired_at)) THEN RAISE EXCEPTION 'database network hold owner mismatch' USING ERRCODE='23514'; END IF;
  IF TG_OP='UPDATE' AND NEW.operation_id IS DISTINCT FROM OLD.operation_id AND (OLD.operation_id<>c.upgrade_operation_id OR NEW.operation_id<>c.application_operation_id OR NOT EXISTS(SELECT 1 FROM hosting_operations o JOIN hosting_database_membership_heads head ON head.connection_id=c.id WHERE o.id=c.upgrade_operation_id AND o.state='succeeded')) THEN RAISE EXCEPTION 'unresolved network hold cannot transfer to another operation' USING ERRCODE='23514'; END IF;
  RETURN NEW;
 END IF;
 IF TG_TABLE_NAME='hosting_database_deployments' THEN binding_key:=NEW.binding_id; operation_key:=NEW.operation_id;
 ELSE
  SELECT o.binding_id,o.id INTO binding_key,operation_key FROM hosting_operations o WHERE o.id=NEW.operation_id;
 END IF;
 SELECT * INTO h FROM hosting_database_network_holds WHERE database_binding_id=binding_key;
 IF FOUND AND h.operation_id IS DISTINCT FROM operation_key THEN RAISE EXCEPTION 'database network is held by another operation' USING ERRCODE='23514'; END IF;
 IF TG_TABLE_NAME='hosting_database_deployments' AND EXISTS(SELECT 1 FROM hosting_database_membership_heads WHERE database_binding_id=binding_key) AND NOT EXISTS(SELECT 1 FROM hosting_database_connection_executions e JOIN hosting_database_connections stored_connection ON stored_connection.id=e.connection_id JOIN hosting_database_bindings b ON b.id=stored_connection.binding_id WHERE e.operation_id=operation_key AND e.family='maintenance' AND b.database_binding_id=binding_key) THEN RAISE EXCEPTION 'connected database requires member-aware maintenance' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER hosting_connection_single_family BEFORE INSERT ON hosting_database_bindings FOR EACH ROW EXECUTE FUNCTION nerocd_database_connection_hold_guard();
CREATE TRIGGER hosting_connection_hold_identity BEFORE INSERT OR UPDATE ON hosting_database_network_holds FOR EACH ROW EXECUTE FUNCTION nerocd_database_connection_hold_guard();
CREATE TRIGGER hosting_connection_database_hold BEFORE INSERT ON hosting_database_deployments FOR EACH ROW EXECUTE FUNCTION nerocd_database_connection_hold_guard();

CREATE FUNCTION nerocd_database_connection_stage_validate() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE e hosting_database_connection_executions; p jsonb; previous hosting_application_database_stages; expected text; lease run_leases; ready hosting_application_database_stages;
BEGIN
 SELECT * INTO STRICT e FROM hosting_database_connection_executions WHERE operation_id=NEW.operation_id AND family='application';
 SELECT body INTO STRICT p FROM hosting_apply_plans WHERE operation_id=e.operation_id;
 SELECT * INTO STRICT lease FROM run_leases WHERE id=NEW.authority->>'lease_id';
 IF lease.status<>'active' OR lease.expires_at<=clock_timestamp() OR lease.run_id IS DISTINCT FROM NEW.authority->>'run_id' OR lease.runner_id IS DISTINCT FROM p->'snapshot'->>'runner_id' OR lease.attempt::text IS DISTINCT FROM NEW.authority->>'attempt' OR lease.fence IS DISTINCT FROM NEW.authority->>'fence' OR p->>'hash' IS DISTINCT FROM NEW.stage->>'plan_hash' OR NEW.stage->'candidate'->>'operation_id' IS DISTINCT FROM e.operation_id OR NEW.stage->'candidate'->>'plan_hash' IS DISTINCT FROM p->>'hash' OR NEW.stage->'candidate'->>'create_nonce' IS DISTINCT FROM e.proof->>'create_nonce' OR NEW.stage->'candidate'->>'image_id' IS DISTINCT FROM p->'release'->>'image_id' OR NEW.stage->'candidate'->>'daemon_id' IS DISTINCT FROM p->'release'->>'daemon_id' OR (NEW.stage->'candidate'->>'container_id' ~ '^[0-9a-f]{64}$') IS DISTINCT FROM true OR NEW.stage->'attach_plan'->>'operation_id' IS DISTINCT FROM e.operation_id OR NEW.stage->'attach_plan'->>'action' IS DISTINCT FROM 'attach' OR NEW.stage->'attach_plan'->>'binding_hash' IS DISTINCT FROM e.proof->'binding'->>'hash' OR NEW.stage->'attach_plan'->>'membership_revision' IS DISTINCT FROM (e.expected_membership_revision+1)::text OR NEW.stage->'attach_plan'->'target'->>'container_id' IS DISTINCT FROM NEW.stage->'candidate'->>'container_id' OR NEW.stage->'attach_plan'->'target'->>'network_id' IS DISTINCT FROM e.proof->'origin'->>'network_id' THEN RAISE EXCEPTION 'connection stage candidate or authority mismatch' USING ERRCODE='23514'; END IF;
 IF NEW.previous_hash IS NULL THEN
  IF NEW.phase<>'candidate_created' OR EXISTS(SELECT 1 FROM hosting_application_database_stages WHERE operation_id=e.operation_id) THEN RAISE EXCEPTION 'connection stage must begin with candidate' USING ERRCODE='23514'; END IF;
 ELSE
  SELECT * INTO STRICT previous FROM hosting_application_database_stages WHERE operation_id=e.operation_id AND stage_hash=NEW.previous_hash;
  expected:=CASE previous.phase WHEN 'candidate_created' THEN 'attach_intent' WHEN 'attach_intent' THEN 'network_configured' WHEN 'network_configured' THEN 'attached' WHEN 'attached' THEN 'ready' END;
  IF previous.phase IN('ready','uncertain') OR NEW.phase NOT IN(expected,'uncertain') OR NEW.stage->'candidate' IS DISTINCT FROM previous.stage->'candidate' OR NEW.stage->'attach_plan' IS DISTINCT FROM previous.stage->'attach_plan' THEN RAISE EXCEPTION 'connection stage order or candidate changed' USING ERRCODE='23514'; END IF;
 END IF;
 IF NEW.phase IN('attach_intent','network_configured','attached','ready') AND (NEW.stage->'endpoint_receipt'->>'plan_hash' IS DISTINCT FROM NEW.stage->'attach_plan'->>'hash' OR NEW.stage->'endpoint_receipt'->>'phase' IS DISTINCT FROM CASE WHEN NEW.phase IN('attached','ready') THEN 'attached' ELSE 'attach_intent' END) THEN RAISE EXCEPTION 'connection stage endpoint phase mismatch' USING ERRCODE='23514'; END IF;
 IF NEW.phase IN('network_configured','ready') AND NEW.stage->'endpoint_receipt' IS DISTINCT FROM previous.stage->'endpoint_receipt' THEN RAISE EXCEPTION 'connection stage endpoint receipt changed' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER hosting_connection_stage_order BEFORE INSERT ON hosting_application_database_stages FOR EACH ROW EXECUTE FUNCTION nerocd_database_connection_stage_validate();

CREATE FUNCTION nerocd_database_connection_effect_validate() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE e hosting_database_connection_executions; o hosting_operations; lease run_leases; latest hosting_application_database_stages; p jsonb; b hosting_database_bindings;
BEGIN
 SELECT * INTO STRICT e FROM hosting_database_connection_executions WHERE operation_id=NEW.operation_id AND family='application';
 SELECT * INTO STRICT o FROM hosting_operations WHERE id=e.operation_id AND state IN('applying','verifying');
 SELECT body INTO STRICT p FROM hosting_apply_plans WHERE operation_id=o.id;
 SELECT * INTO STRICT b FROM hosting_database_bindings WHERE id=e.proof->'binding'->>'id';
 SELECT * INTO STRICT lease FROM run_leases WHERE id=NEW.request->>'lease_id' FOR SHARE;
 SELECT * INTO latest FROM hosting_application_database_stages WHERE operation_id=o.id ORDER BY created_at DESC,receipt_id DESC LIMIT 1;
 IF lease.status<>'active' OR lease.expires_at<=clock_timestamp() OR lease.run_id<>o.run_id OR lease.run_id IS DISTINCT FROM NEW.request->>'run_id' OR lease.runner_id IS DISTINCT FROM p->'snapshot'->>'runner_id' OR lease.attempt::text IS DISTINCT FROM NEW.request->>'attempt' OR lease.fence IS DISTINCT FROM NEW.request->>'fence' OR NEW.response->>'plan_hash' IS DISTINCT FROM p->>'hash' OR NEW.response->>'membership_revision' IS DISTINCT FROM e.expected_membership_revision::text OR (NEW.response->>'use_until')::timestamptz IS DISTINCT FROM NEW.use_until OR NEW.use_until>least(lease.expires_at,clock_timestamp()+interval '30 seconds') OR NEW.use_until<=clock_timestamp() OR NOT EXISTS(SELECT 1 FROM hosting_database_network_holds h WHERE h.operation_id=o.id AND h.database_binding_id=b.database_binding_id) OR NOT EXISTS(SELECT 1 FROM hosting_binding_state state WHERE state.binding_id=o.binding_id AND state.hold_operation_id=o.id) THEN RAISE EXCEPTION 'connection effect authority mismatch' USING ERRCODE='23514'; END IF;
 IF NOT ((NEW.request->>'action'='create' AND latest.operation_id IS NULL AND NEW.request->>'expected_stage_hash'='') OR (NEW.request->>'expected_stage_hash'=latest.stage_hash AND ((NEW.request->>'action'='configure' AND latest.phase='attach_intent') OR (NEW.request->>'action'='start' AND latest.phase='network_configured') OR (NEW.request->>'action'='retire' AND latest.phase='ready' AND EXISTS(SELECT 1 FROM hosting_application_database_retirements r WHERE r.operation_id=o.id AND r.phase='retire_intent') AND NOT EXISTS(SELECT 1 FROM hosting_application_database_retirements r WHERE r.operation_id=o.id AND r.phase='detached'))))) THEN RAISE EXCEPTION 'connection effect does not match current stage' USING ERRCODE='23514'; END IF;
 PERFORM nerocd_database_connection_validate(o.id,true);
 RETURN NEW;
END $$;
CREATE TRIGGER hosting_connection_effect_authority BEFORE INSERT ON hosting_application_database_effect_authorizations FOR EACH ROW EXECUTE FUNCTION nerocd_database_connection_effect_validate();

CREATE FUNCTION nerocd_database_connection_schema_compatible() RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT NOT EXISTS(SELECT 1 FROM (VALUES
 ('hosting_database_connections','hosting_database_connection_identity'),
 ('hosting_database_connections','hosting_connection_request_consistency'),
 ('hosting_database_connection_executions','hosting_connection_executions_immutable'),
 ('hosting_database_connection_executions','hosting_connection_execution_consistency'),
 ('hosting_snapshots','hosting_connection_snapshot_consistency'),
 ('hosting_apply_plans','hosting_connection_plan_consistency'),
 ('hosting_database_network_holds','hosting_connection_hold_identity'),
 ('hosting_database_bindings','hosting_connection_single_family'),
 ('hosting_database_deployments','hosting_connection_database_hold'),
 ('hosting_application_database_stages','hosting_connection_stages_immutable'),
 ('hosting_application_database_stages','hosting_connection_stage_order'),
 ('hosting_application_database_effect_authorizations','hosting_connection_effect_authority'),
 ('hosting_application_database_retirements','hosting_connection_retirements_immutable'),
 ('hosting_application_database_retirements','hosting_connection_retirement_order'),
 ('hosting_database_membership_heads','hosting_connection_membership_advance'),
 ('hosting_database_network_holds','hosting_connection_hold_release'),
 ('run_leases','hosting_connection_lease_guard'),
 ('hosting_database_membership_receipts','hosting_connection_membership_receipts_immutable'),
 ('hosting_application_database_effect_authorizations','hosting_connection_effect_authorizations_immutable'),
 ('hosting_database_connection_recoveries','hosting_connection_recoveries_immutable')) required(table_name,trigger_name)
 WHERE NOT EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass(current_schema()||'.'||table_name) AND tgname=trigger_name AND tgenabled IN('O','A')));
$$;

CREATE FUNCTION nerocd_database_connection_lease_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE e hosting_database_connection_executions; runner runners; target hosting_task_targets; c hosting_database_connections; binding_key text;
BEGIN
 SELECT * INTO target FROM hosting_task_targets WHERE run_id=NEW.run_id;
 IF NOT FOUND THEN RETURN NEW; END IF;
 SELECT * INTO e FROM hosting_database_connection_executions WHERE operation_id=target.operation_id;
 IF NOT FOUND THEN RETURN NEW; END IF;
 SELECT * INTO STRICT c FROM hosting_database_connections WHERE id=e.connection_id;
 SELECT b.database_binding_id INTO STRICT binding_key FROM hosting_database_bindings b WHERE b.id=c.binding_id;
 SELECT * INTO STRICT runner FROM runners WHERE id=NEW.runner_id FOR SHARE;
 IF runner.status<>'active' OR runner.token_hash='' OR 'shell'=ANY(runner.capabilities) OR target.runner_id<>runner.id OR NOT EXISTS(SELECT 1 FROM runner_enrollments n WHERE n.runner_id=runner.id AND n.used_at IS NOT NULL AND n.revoked_at IS NULL AND n.credential_hash=runner.token_hash) OR NOT EXISTS(SELECT 1 FROM hosting_database_network_holds h WHERE h.database_binding_id=binding_key AND h.connection_id=c.id AND h.operation_id=e.operation_id) THEN RAISE EXCEPTION 'connection lease target or hold mismatch' USING ERRCODE='23514'; END IF;
 IF e.family='application' THEN
  IF NOT ('hosting_application_database_v1'=ANY(runner.capabilities) AND 'hosting_runtime_env_v1'=ANY(runner.capabilities) AND 'image_release_ports_v1'=ANY(runner.capabilities)) THEN RAISE EXCEPTION 'connected application capability required' USING ERRCODE='23514'; END IF;
 ELSE
  IF NOT ('hosting_database_membership_maintenance_v1'=ANY(runner.capabilities) AND 'hosting_database_v1'=ANY(runner.capabilities) AND 'hosting_owned_storage_v1'=ANY(runner.capabilities) AND 'database_deploy'=ANY(runner.capabilities)) THEN RAISE EXCEPTION 'member maintenance capability required' USING ERRCODE='23514'; END IF;
 END IF;
 PERFORM nerocd_database_connection_validate(e.operation_id,false);
 RETURN NEW;
END $$;
CREATE TRIGGER hosting_connection_lease_guard BEFORE INSERT ON run_leases FOR EACH ROW EXECUTE FUNCTION nerocd_database_connection_lease_guard();

CREATE FUNCTION nerocd_database_connection_retirement_validate() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE e hosting_database_connection_executions; ready hosting_application_database_stages; intent hosting_application_database_retirements; prior jsonb; lease run_leases;
BEGIN
 SELECT * INTO STRICT e FROM hosting_database_connection_executions WHERE operation_id=NEW.operation_id AND family='application';
 SELECT * INTO STRICT ready FROM hosting_application_database_stages WHERE operation_id=NEW.operation_id AND phase='ready';
 IF EXISTS(SELECT 1 FROM hosting_application_database_stages WHERE operation_id=NEW.operation_id AND phase='uncertain') OR jsonb_array_length(e.proof->'members')<>1 THEN RAISE EXCEPTION 'retirement requires ready replacement and one predecessor' USING ERRCODE='23514'; END IF;
 prior:=e.proof->'members'->0;
 SELECT * INTO STRICT lease FROM run_leases WHERE id=NEW.authority->>'lease_id';
 IF lease.run_id IS DISTINCT FROM NEW.authority->>'run_id' OR lease.attempt::text IS DISTINCT FROM NEW.authority->>'attempt' OR lease.fence IS DISTINCT FROM NEW.authority->>'fence' OR NEW.plan->'target' IS DISTINCT FROM prior->'endpoint_plan'->'target' OR NEW.plan->>'expected_endpoint_id' IS DISTINCT FROM prior->'receipt'->'observation'->'endpoint'->>'endpoint_id' OR NEW.plan->>'membership_revision' IS DISTINCT FROM (e.expected_membership_revision+2)::text OR NEW.plan->>'binding_hash' IS DISTINCT FROM e.proof->'binding'->>'hash' OR NEW.receipt->>'binding_hash' IS DISTINCT FROM e.proof->'binding'->>'hash' OR NEW.receipt->>'membership_revision' IS DISTINCT FROM NEW.plan->>'membership_revision' THEN RAISE EXCEPTION 'retirement predecessor or authority mismatch' USING ERRCODE='23514'; END IF;
 IF NEW.phase='detached' THEN
  SELECT * INTO STRICT intent FROM hosting_application_database_retirements WHERE operation_id=NEW.operation_id AND phase='retire_intent';
  IF NEW.plan IS DISTINCT FROM intent.plan OR NEW.authority IS DISTINCT FROM intent.authority OR NEW.receipt->'observation'->'endpoint' IS DISTINCT FROM intent.receipt->'intent' OR NEW.receipt->'observation'->'present' IS DISTINCT FROM 'false'::jsonb THEN RAISE EXCEPTION 'retirement detachment does not match exact intent' USING ERRCODE='23514'; END IF;
 ELSE
  IF NEW.receipt->'intent' IS DISTINCT FROM prior->'receipt'->'observation'->'endpoint' THEN RAISE EXCEPTION 'retirement intent changed attached endpoint' USING ERRCODE='23514'; END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER hosting_connection_retirement_order BEFORE INSERT ON hosting_application_database_retirements FOR EACH ROW EXECUTE FUNCTION nerocd_database_connection_retirement_validate();

CREATE FUNCTION nerocd_database_membership_advance() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE attached hosting_database_membership_receipts; retired_receipt hosting_database_membership_receipts; stage hosting_application_database_stages; e hosting_database_connection_executions; healthy jsonb;
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'membership head cannot be deleted' USING ERRCODE='23514'; END IF;
 IF TG_OP='INSERT' THEN
  IF NEW.revision<>1 OR NEW.members<>'[]'::jsonb OR NEW.retired<>'[]'::jsonb OR NOT EXISTS(SELECT 1 FROM hosting_database_connections c JOIN hosting_database_bindings b ON b.id=c.binding_id JOIN hosting_operations o ON o.id=c.upgrade_operation_id JOIN hosting_apply_plans p ON p.operation_id=o.id JOIN hosting_database_network_origin_receipts receipt ON receipt.capture_id=c.capture_id WHERE c.id=NEW.connection_id AND b.database_binding_id=NEW.database_binding_id AND o.state='succeeded' AND receipt.receipt_id=NEW.origin_receipt_id AND p.body->'database'->'network'->'members'='[]'::jsonb AND p.body->'database'->'network'->>'membership_revision'='1') THEN RAISE EXCEPTION 'membership head requires settled empty upgrade' USING ERRCODE='23514'; END IF;
  RETURN NEW;
 END IF;
 IF (NEW.database_binding_id,NEW.connection_id,NEW.origin_receipt_id) IS DISTINCT FROM (OLD.database_binding_id,OLD.connection_id,OLD.origin_receipt_id) OR NEW.revision NOT IN(OLD.revision+1,OLD.revision+2) THEN RAISE EXCEPTION 'membership revision or identity mismatch' USING ERRCODE='23514'; END IF;
 SELECT * INTO STRICT attached FROM hosting_database_membership_receipts WHERE database_binding_id=OLD.database_binding_id AND revision=OLD.revision+1 AND action='attach';
 SELECT * INTO STRICT e FROM hosting_database_connection_executions WHERE operation_id=attached.operation_id AND connection_id=OLD.connection_id AND family='application';
 SELECT * INTO STRICT stage FROM hosting_application_database_stages WHERE operation_id=e.operation_id AND phase='ready';
 SELECT bs.healthy INTO STRICT healthy FROM hosting_binding_state bs JOIN hosting_operations op ON op.binding_id=bs.binding_id WHERE op.id=e.operation_id AND op.state='succeeded' AND bs.healthy->>'operation_id'=op.id;
 IF e.expected_membership_revision<>OLD.revision OR attached.body->'binding' IS DISTINCT FROM e.proof->'binding' OR attached.body->'endpoint_plan' IS DISTINCT FROM stage.stage->'attach_plan' OR attached.body->'receipt' IS DISTINCT FROM stage.stage->'endpoint_receipt' OR attached.body->'application_healthy' IS DISTINCT FROM healthy OR NEW.members IS DISTINCT FROM jsonb_build_array(attached.body) OR NOT EXISTS(SELECT 1 FROM hosting_database_network_holds h WHERE h.database_binding_id=OLD.database_binding_id AND h.operation_id=e.operation_id AND h.connection_id=OLD.connection_id) THEN RAISE EXCEPTION 'membership attachment requires atomic healthy settlement' USING ERRCODE='23514'; END IF;
 IF NEW.revision=OLD.revision+2 THEN
  SELECT * INTO STRICT retired_receipt FROM hosting_database_membership_receipts WHERE database_binding_id=OLD.database_binding_id AND revision=NEW.revision AND operation_id=e.operation_id AND action='retire';
  IF jsonb_array_length(OLD.members)<>1 OR retired_receipt.body->'attached' IS DISTINCT FROM OLD.members->0 OR NEW.retired IS DISTINCT FROM OLD.retired||jsonb_build_array(retired_receipt.body) OR NOT EXISTS(SELECT 1 FROM hosting_application_database_retirements intent JOIN hosting_application_database_retirements detached ON detached.operation_id=intent.operation_id AND detached.phase='detached' WHERE intent.operation_id=e.operation_id AND intent.phase='retire_intent' AND retired_receipt.body->'retire_plan'=intent.plan AND retired_receipt.body->'retire_intent'=intent.receipt AND retired_receipt.body->'detached'=detached.receipt) THEN RAISE EXCEPTION 'membership retirement lacks immutable detachment proof' USING ERRCODE='23514'; END IF;
 ELSIF OLD.members<>'[]'::jsonb OR NEW.retired IS DISTINCT FROM OLD.retired THEN RAISE EXCEPTION 'replacement cannot omit predecessor retirement' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER hosting_connection_membership_advance BEFORE INSERT OR UPDATE OR DELETE ON hosting_database_membership_heads FOR EACH ROW EXECUTE FUNCTION nerocd_database_membership_advance();

CREATE FUNCTION nerocd_database_connection_hold_release() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE e hosting_database_connection_executions;
BEGIN
 SELECT * INTO STRICT e FROM hosting_database_connection_executions WHERE operation_id=OLD.operation_id AND connection_id=OLD.connection_id;
 IF NOT EXISTS(SELECT 1 FROM hosting_operations o JOIN hosting_receipts r ON r.operation_id=o.id AND r.phase='complete' WHERE o.id=e.operation_id AND o.state='succeeded' AND r.body->>'outcome'='succeeded' AND r.body->>'ready'='true' AND coalesce(r.body->>'uncertain','false')='false') THEN RAISE EXCEPTION 'unresolved database network hold cannot be released' USING ERRCODE='23514'; END IF;
 IF e.family='application' AND NOT EXISTS(SELECT 1 FROM hosting_database_membership_heads head JOIN hosting_database_membership_receipts receipt ON receipt.database_binding_id=head.database_binding_id AND receipt.revision=head.revision WHERE head.database_binding_id=OLD.database_binding_id AND head.connection_id=OLD.connection_id AND receipt.operation_id=e.operation_id) THEN RAISE EXCEPTION 'network hold release requires atomic membership settlement' USING ERRCODE='23514'; END IF;
 RETURN OLD;
END $$;
CREATE TRIGGER hosting_connection_hold_release BEFORE DELETE ON hosting_database_network_holds FOR EACH ROW EXECUTE FUNCTION nerocd_database_connection_hold_release();
CREATE TRIGGER hosting_database_connection_identity BEFORE UPDATE OR DELETE ON hosting_database_connections FOR EACH ROW EXECUTE FUNCTION nerocd_database_connection_identity();

CREATE FUNCTION nerocd_database_connection_flush() RETURNS void LANGUAGE plpgsql AS $$ BEGIN SET CONSTRAINTS ALL IMMEDIATE; END $$;
CREATE FUNCTION nerocd_database_connection_defer_admission() RETURNS void LANGUAGE plpgsql AS $$ BEGIN SET CONSTRAINTS hosting_connection_request_consistency DEFERRED; END $$;

-- Cumulative retirement evidence is retained in the head, while a compiler
-- transition includes only members removed from its actual DB predecessor.
CREATE FUNCTION nerocd_database_connection_transition_retired(prior_plan jsonb,members jsonb,ledger jsonb) RETURNS jsonb LANGUAGE sql IMMUTABLE AS $$
 SELECT coalesce(jsonb_agg(r.value ORDER BY r.ordinality),'[]'::jsonb)
 FROM jsonb_array_elements(ledger) WITH ORDINALITY r(value,ordinality)
 WHERE EXISTS(SELECT 1 FROM jsonb_array_elements(prior_plan->'database'->'network'->'members') m WHERE m->>'endpoint_plan_hash'=r.value->'attached'->'endpoint_plan'->>'hash' AND m->>'receipt_hash'=r.value->'attached'->'receipt'->>'hash')
 AND NOT EXISTS(SELECT 1 FROM jsonb_array_elements(members) m WHERE m->'endpoint_plan'->>'hash'=r.value->'attached'->'endpoint_plan'->>'hash' AND m->'receipt'->>'hash'=r.value->'attached'->'receipt'->>'hash');
$$;

CREATE FUNCTION nerocd_database_connection_validate(operation_key text, admission boolean) RETURNS void LANGUAGE plpgsql AS $$
DECLARE e hosting_database_connection_executions; c hosting_database_connections; b hosting_database_bindings; o hosting_operations; head hosting_database_membership_heads; snap jsonb; plan jsonb; db_plan jsonb; db_healthy jsonb; captured jsonb; origin jsonb; original_plan jsonb;
BEGIN
 SELECT * INTO STRICT e FROM hosting_database_connection_executions WHERE operation_id=operation_key;
 SELECT * INTO STRICT c FROM hosting_database_connections WHERE id=e.connection_id;
 SELECT * INTO STRICT b FROM hosting_database_bindings WHERE id=c.binding_id;
 SELECT * INTO STRICT o FROM hosting_operations WHERE id=e.operation_id;
 SELECT * INTO STRICT head FROM hosting_database_membership_heads WHERE connection_id=c.id;
 SELECT body INTO STRICT snap FROM hosting_snapshots WHERE operation_id=o.id;
 SELECT body INTO STRICT original_plan FROM hosting_apply_plans WHERE operation_id=b.database_operation_id;
 SELECT capture.plan INTO STRICT captured FROM hosting_database_network_captures capture WHERE capture.id=c.capture_id;
 SELECT receipt INTO STRICT origin FROM hosting_database_network_origin_receipts WHERE capture_id=c.capture_id AND receipt_id=head.origin_receipt_id;
 IF b.application_resource_id<>c.application_resource_id OR b.database_resource_id<>c.database_resource_id OR b.project_id<>c.project_id OR captured->>'source_operation_id'<>c.source_operation_id OR captured->'source_plan' IS DISTINCT FROM original_plan OR origin->>'operation_id' IS DISTINCT FROM c.capture_id THEN RAISE EXCEPTION 'connection retained binding or capture mismatch' USING ERRCODE='23514'; END IF;
 IF e.family='application' THEN
  db_plan:=e.proof->'database_plan'; db_healthy:=e.proof->'database_healthy';
  IF o.resource_id<>c.application_resource_id OR o.binding_id<>b.application_binding_id OR o.project_id<>c.project_id OR e.proof->'binding' IS DISTINCT FROM b.body OR e.proof->'binding_database_plan' IS DISTINCT FROM original_plan OR e.proof->'binding_application_snapshot' IS DISTINCT FROM c.binding_application_snapshot OR e.proof->'origin' IS DISTINCT FROM origin->'origin' OR snap->>'policy_version'<>'public-image.runtime-env.database.release-ports.v1' OR snap->'application_database' IS DISTINCT FROM jsonb_build_object('binding_id',b.id,'binding_hash',b.body_hash) OR snap->'runtime_configuration' IS DISTINCT FROM b.body->'runtime_configuration' OR snap ?| ARRAY['database','storage','storage_route_continuity','local_build'] OR e.proof - 'version' - 'operation_id' - 'create_nonce' - 'binding' - 'application_project_id' - 'database_project_id' - 'binding_application_snapshot' - 'binding_database_plan' - 'database_plan' - 'database_healthy' - 'origin' - 'membership_revision' - 'members' - 'retired' - 'hash'<>'{}'::jsonb OR e.proof->>'application_project_id'<>c.project_id OR e.proof->>'database_project_id'<>c.project_id THEN RAISE EXCEPTION 'application connection proof or family mismatch' USING ERRCODE='23514'; END IF;
 ELSE
  db_plan:=e.proof->'previous_plan'; db_healthy:=e.proof->'previous_healthy';
  IF o.resource_id<>c.database_resource_id OR o.binding_id<>b.database_binding_id OR o.project_id<>c.project_id OR e.proof->'capture_plan' IS DISTINCT FROM captured OR e.proof->'origin_receipt' IS DISTINCT FROM origin OR e.proof - 'version' - 'capture_plan' - 'origin_receipt' - 'previous_plan' - 'previous_healthy' - 'membership_revision' - 'members' - 'retired' - 'hash'<>'{}'::jsonb OR snap IS DISTINCT FROM db_plan->'snapshot' OR EXISTS(SELECT 1 FROM hosting_database_network_upgrades WHERE operation_id=o.id) THEN RAISE EXCEPTION 'database maintenance proof or family mismatch' USING ERRCODE='23514'; END IF;
 END IF;
 IF jsonb_typeof(e.proof->'members') IS DISTINCT FROM 'array' OR jsonb_array_length(e.proof->'members')>1 OR jsonb_typeof(e.proof->'retired') IS DISTINCT FROM 'array' OR db_plan->>'compiler_version'<>'database-hosting.private.membership.v2' OR db_plan->'database'->'network'->>'network_id' IS DISTINCT FROM origin->'origin'->>'network_id' OR db_plan->'database'->'network'->>'origin_hash' IS DISTINCT FROM origin->>'origin_hash' OR NOT EXISTS(SELECT 1 FROM hosting_apply_plans p JOIN hosting_operations prior ON prior.id=p.operation_id JOIN hosting_receipts receipt ON receipt.operation_id=prior.id AND receipt.phase='complete' WHERE prior.id=db_healthy->>'operation_id' AND prior.state='succeeded' AND prior.resource_id=c.database_resource_id AND p.body=db_plan AND p.body_hash=db_healthy->>'plan_hash' AND receipt.body->>'container_id'=db_healthy->>'container_id' AND receipt.body->>'ready'='true') THEN RAISE EXCEPTION 'connection database predecessor association mismatch' USING ERRCODE='23514'; END IF;
 IF NOT EXISTS(SELECT 1 FROM audit_events a WHERE a.id=e.audit_id AND a.target_id=o.id AND a.actor_id=o.created_by AND a.metadata->>'proof_hash'=e.proof_hash AND a.action=CASE e.family WHEN 'application' THEN 'hosting.application_database.requested' ELSE 'hosting.database_maintenance.requested' END) THEN RAISE EXCEPTION 'connection execution audit mismatch' USING ERRCODE='23514'; END IF;
 IF admission AND (head.revision<>e.expected_membership_revision OR e.proof->'members' IS DISTINCT FROM head.members OR e.proof->'retired' IS DISTINCT FROM nerocd_database_connection_transition_retired(db_plan,head.members,head.retired) OR db_healthy IS DISTINCT FROM (SELECT healthy FROM hosting_binding_state WHERE binding_id=b.database_binding_id) OR NOT EXISTS(SELECT 1 FROM hosting_database_network_holds h WHERE h.database_binding_id=b.database_binding_id AND h.connection_id=c.id AND h.operation_id=o.id)) THEN RAISE EXCEPTION 'connection current membership or hold mismatch' USING ERRCODE='23514'; END IF;
 SELECT body INTO plan FROM hosting_apply_plans WHERE operation_id=o.id;
 IF FOUND THEN
  IF plan->'snapshot' IS DISTINCT FROM snap OR plan->>'hash' IS DISTINCT FROM (SELECT body_hash FROM hosting_apply_plans WHERE operation_id=o.id) THEN RAISE EXCEPTION 'connection plan snapshot mismatch' USING ERRCODE='23514'; END IF;
  IF e.family='application' THEN
   IF plan->>'compiler_version'<>'image-hosting.runtime-env.database.v1' OR plan->'application_database'->'binding' IS DISTINCT FROM b.body OR plan->'application_database'->>'operation_id'<>o.id OR plan->'application_database'->>'create_nonce' IS DISTINCT FROM e.proof->>'create_nonce' OR plan->'application_database'->>'database_plan_hash' IS DISTINCT FROM db_plan->>'hash' OR plan->'application_database'->'origin' IS DISTINCT FROM origin->'origin' OR plan->'application_database'->>'membership_revision' IS DISTINCT FROM e.expected_membership_revision::text OR plan ?| ARRAY['database','storage'] THEN RAISE EXCEPTION 'application connection plan substitution' USING ERRCODE='23514'; END IF;
  ELSE
   IF plan->>'compiler_version'<>'database-hosting.private.membership.v2' OR plan->'release' IS DISTINCT FROM db_plan->'release' OR plan->'storage' IS DISTINCT FROM db_plan->'storage' OR (plan->'database')-'network' IS DISTINCT FROM (db_plan->'database')-'network' OR plan->'previous_healthy' IS DISTINCT FROM db_healthy OR plan->'database'->'network'->>'membership_revision' IS DISTINCT FROM e.expected_membership_revision::text OR plan->'database'->'network'->>'network_id' IS DISTINCT FROM origin->'origin'->>'network_id' OR plan ? 'application_database' THEN RAISE EXCEPTION 'member maintenance plan substitution' USING ERRCODE='23514'; END IF;
  END IF;
 END IF;
END $$;

CREATE FUNCTION nerocd_database_connection_consistent() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_TABLE_NAME='hosting_database_connection_executions' THEN PERFORM nerocd_database_connection_validate(NEW.operation_id,true);
 ELSIF EXISTS(SELECT 1 FROM hosting_database_connection_executions WHERE operation_id=NEW.operation_id) THEN PERFORM nerocd_database_connection_validate(NEW.operation_id,false);
 ELSIF NEW.body ? 'application_database' OR NEW.body->>'policy_version'='public-image.runtime-env.database.release-ports.v1' OR NEW.body->>'compiler_version'='image-hosting.runtime-env.database.v1' THEN RAISE EXCEPTION 'connected application requires immutable execution proof' USING ERRCODE='23514'; END IF;
 RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER hosting_connection_execution_consistency AFTER INSERT ON hosting_database_connection_executions DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION nerocd_database_connection_consistent();
CREATE CONSTRAINT TRIGGER hosting_connection_snapshot_consistency AFTER INSERT ON hosting_snapshots DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION nerocd_database_connection_consistent();
CREATE CONSTRAINT TRIGGER hosting_connection_plan_consistency AFTER INSERT ON hosting_apply_plans DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION nerocd_database_connection_consistent();

-- Pending bindings keep the original guard for every unqualified family and
-- runtime edit. An application sidecar is inserted first in the same transaction;
-- deferred association checks require its complete operation, snapshot and audit.
CREATE OR REPLACE FUNCTION nerocd_database_binding_pending_effect() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE resource_key text; operation_key text;
BEGIN
 IF TG_TABLE_NAME='hosting_runtime_configuration_versions' THEN
  resource_key:=NEW.resource_id;
  IF TG_WHEN='AFTER' AND EXISTS(SELECT 1 FROM hosting_database_bindings b WHERE b.application_resource_id=resource_key AND b.runtime_generation=NEW.generation AND b.xmin::text::bigint=mod(txid_current(),4294967296)) THEN RETURN NEW; END IF;
 ELSIF TG_TABLE_NAME='hosting_operations' THEN resource_key:=NEW.resource_id; operation_key:=NEW.id;
 ELSIF TG_TABLE_NAME='run_leases' THEN SELECT o.resource_id,o.id INTO resource_key,operation_key FROM hosting_task_targets t JOIN hosting_operations o ON o.id=t.operation_id WHERE t.run_id=NEW.run_id AND t.mode='deploy';
 ELSIF TG_TABLE_NAME='audit_events' THEN
  IF NEW.action<>'hosting.runtime_environment_authorized' THEN RETURN NEW; END IF;
  SELECT resource_id,id INTO resource_key,operation_key FROM hosting_operations WHERE id=NEW.target_id;
 ELSE SELECT resource_id,id INTO resource_key,operation_key FROM hosting_operations WHERE id=NEW.operation_id;
 END IF;
 IF EXISTS(SELECT 1 FROM hosting_database_bindings WHERE application_resource_id=resource_key) THEN
  IF operation_key IS NOT NULL AND EXISTS(SELECT 1 FROM hosting_database_connection_executions e JOIN hosting_database_connections c ON c.id=e.connection_id JOIN hosting_database_bindings b ON b.id=c.binding_id WHERE e.operation_id=operation_key AND e.family='application' AND c.application_resource_id=resource_key AND e.proof->'binding'=b.body AND e.proof->>'operation_id'=operation_key AND e.proof->>'version'='application-database.execution.v1') THEN
   IF TG_WHEN='AFTER' THEN PERFORM nerocd_database_connection_validate(operation_key,false); END IF;
   RETURN NEW;
  END IF;
  RAISE EXCEPTION 'database binding connectivity is pending' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;

-- Preserve existing artifact/runtime family checks after the exact connected branch.
CREATE OR REPLACE FUNCTION nerocd_hosting_build_deployment_consistent() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE op_key text; snap jsonb; rel jsonb; artifact jsonb; artifact_hash text; build_row hosting_builds; op_row hosting_operations; association hosting_operation_build_artifacts;
BEGIN
 op_key:=NEW.operation_id;
 SELECT * INTO op_row FROM hosting_operations WHERE id=op_key;
 SELECT body INTO snap FROM hosting_snapshots WHERE operation_id=op_key;
 IF EXISTS(SELECT 1 FROM hosting_database_connection_executions WHERE operation_id=op_key AND family='application') THEN
  PERFORM nerocd_database_connection_validate(op_key,false);
  IF EXISTS(SELECT 1 FROM hosting_operation_build_artifacts WHERE operation_id=op_key) THEN RAISE EXCEPTION 'connected application cannot bind local artifact' USING ERRCODE='23514'; END IF;
  RETURN NULL;
 END IF;
 IF snap ? 'database' OR snap->>'policy_version'='database.private.owned-storage.v1' THEN
  PERFORM nerocd_hosting_database_execution_validate(op_key);
  IF EXISTS(SELECT 1 FROM hosting_operation_build_artifacts WHERE operation_id=op_key) THEN RAISE EXCEPTION 'database cannot bind local build artifact' USING ERRCODE='23514'; END IF;
  RETURN NULL;
 END IF;
 -- Preserve historical rows while binding every newly inserted snapshot to
 -- its immutable runtime document. Current generation and secret liveness are
 -- fresh-admission checks, not historical evidence constraints.
 IF snap ? 'storage' THEN
  IF snap ? 'runtime_configuration' OR snap ? 'local_build' OR snap->>'policy_version' IS DISTINCT FROM 'public-image.owned-storage.release-ports.v1'
  OR NOT EXISTS(SELECT 1 FROM hosting_storage st WHERE st.id=snap->'storage'->>'storage_id' AND st.generation=(snap->'storage'->>'generation')::bigint AND st.body_hash=snap->'storage'->>'hash' AND st.resource_id=op_row.resource_id AND st.project_id=op_row.project_id AND st.binding_id=op_row.binding_id)
  THEN RAISE EXCEPTION 'storage snapshot identity mismatch' USING ERRCODE='23514'; END IF;
 ELSIF snap->>'policy_version'='public-image.owned-storage.release-ports.v1' THEN
  RAISE EXCEPTION 'storage policy requires storage reference' USING ERRCODE='23514';
 ELSIF snap ? 'runtime_configuration' THEN
  IF jsonb_typeof(snap->'runtime_configuration') IS DISTINCT FROM 'object'
  OR ((snap->'runtime_configuration') - 'generation' - 'hash') <> '{}'::jsonb
  OR jsonb_typeof(snap->'runtime_configuration'->'generation') IS DISTINCT FROM 'number'
  OR coalesce(snap->'runtime_configuration'->>'generation','') !~ '^[1-9][0-9]{0,18}$'
  OR coalesce(snap->'runtime_configuration'->>'hash','') !~ '^sha256:[0-9a-f]{64}$' THEN
   RAISE EXCEPTION 'invalid runtime configuration reference' USING ERRCODE='23514';
  END IF;
  IF (snap->'runtime_configuration'->>'generation')::numeric > 9223372036854775807
  OR snap->>'policy_version' IS DISTINCT FROM (CASE WHEN snap ? 'local_build' THEN 'local-build.runtime-env.release-ports.v1' ELSE 'public-image.runtime-env.release-ports.v1' END)
  OR NOT EXISTS(SELECT 1 FROM hosting_runtime_configuration_versions v WHERE v.resource_id=op_row.resource_id AND v.project_id=op_row.project_id AND v.generation=(snap->'runtime_configuration'->>'generation')::numeric AND v.body_hash=snap->'runtime_configuration'->>'hash') THEN
   RAISE EXCEPTION 'runtime configuration reference mismatch' USING ERRCODE='23514';
  END IF;
 ELSE
  IF (snap ? 'local_build' AND snap->>'policy_version' IS DISTINCT FROM 'local-build.release-ports.v1')
  OR (NOT (snap ? 'local_build') AND coalesce(snap->>'policy_version','') NOT IN ('public-image.loopback.v1','public-image.release-ports.v1')) THEN
   RAISE EXCEPTION 'hosting policy requires matching runtime reference' USING ERRCODE='23514';
  END IF;
 END IF;
 SELECT * INTO association FROM hosting_operation_build_artifacts WHERE operation_id=op_key;
 IF NOT FOUND THEN
  IF snap ? 'local_build' THEN RAISE EXCEPTION 'local snapshot requires artifact association' USING ERRCODE='23514'; END IF;
  SELECT body INTO rel FROM hosting_releases WHERE operation_id=op_key;
  IF rel ? 'local_build' THEN RAISE EXCEPTION 'local release requires artifact association' USING ERRCODE='23514'; END IF;
  RETURN NULL;
 END IF;
 SELECT * INTO build_row FROM hosting_builds WHERE id=association.build_id;
 SELECT body,body_hash INTO artifact,artifact_hash FROM hosting_build_artifacts WHERE build_id=association.build_id;
 IF build_row.state<>'succeeded' OR build_row.resource_id<>op_row.resource_id OR build_row.project_id<>op_row.project_id
 OR (snap->'local_build'->'artifact') IS DISTINCT FROM artifact OR snap->'local_build'->>'artifact_hash' IS DISTINCT FROM artifact_hash
 OR (snap->'local_build'->>'source_generation')::bigint IS DISTINCT FROM (build_row.snapshot->'source'->>'generation')::bigint
 OR snap->>'image_reference' IS DISTINCT FROM ''
 OR (snap->>'resource_generation')::bigint IS DISTINCT FROM (build_row.snapshot->>'resource_generation')::bigint
 OR snap->>'resource_id' IS DISTINCT FROM op_row.resource_id OR snap->>'binding_id' IS DISTINCT FROM op_row.binding_id
 OR snap->>'binding_id' IS DISTINCT FROM build_row.snapshot->>'binding_id'
 OR snap->>'destination_id' IS DISTINCT FROM build_row.snapshot->>'destination_id'
 OR snap->>'server_id' IS DISTINCT FROM build_row.snapshot->>'server_id'
 OR snap->>'runner_id' IS DISTINCT FROM build_row.snapshot->>'runner_id' THEN
  RAISE EXCEPTION 'local deployment does not match successful artifact target' USING ERRCODE='23514';
 END IF;
 SELECT body INTO rel FROM hosting_releases WHERE operation_id=op_key;
 IF FOUND AND ((rel->'local_build') IS DISTINCT FROM artifact OR rel->>'canonical_image' IS DISTINCT FROM ''
 OR rel->>'image_id' IS DISTINCT FROM artifact->>'image_id' OR rel->>'daemon_id' IS DISTINCT FROM artifact->>'daemon_id'
 OR rel->>'os' IS DISTINCT FROM split_part(artifact->>'platform','/',1) OR rel->>'architecture' IS DISTINCT FROM split_part(artifact->>'platform','/',2)
 OR coalesce(rel->>'variant','')<>'') THEN RAISE EXCEPTION 'local release substitution' USING ERRCODE='23514'; END IF;
 RETURN NULL;
END;$$;

-- Member-aware maintenance is a separate sidecar; empty upgrades stay unchanged.
CREATE OR REPLACE FUNCTION nerocd_database_network_upgrade_consistent() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF EXISTS(SELECT 1 FROM hosting_database_connection_executions WHERE operation_id=NEW.operation_id AND family='maintenance') THEN
  PERFORM nerocd_database_connection_validate(NEW.operation_id,false);
  RETURN NULL;
 END IF;
 IF TG_TABLE_NAME='hosting_database_network_upgrades' THEN
  PERFORM nerocd_database_network_upgrade_validate(NEW.operation_id,true);
 ELSIF EXISTS(SELECT 1 FROM hosting_database_network_upgrades WHERE operation_id=NEW.operation_id) THEN
  PERFORM nerocd_database_network_upgrade_validate(NEW.operation_id,false);
 ELSIF NEW.body->>'compiler_version'='database-hosting.private.membership.v2' OR NEW.body->'database' ? 'network' THEN
  RAISE EXCEPTION 'database v2 requires immutable upgrade proof' USING ERRCODE='23514';
 END IF;
 IF NOT EXISTS(SELECT 1 FROM hosting_database_network_upgrades WHERE operation_id=NEW.operation_id) AND EXISTS(SELECT 1 FROM hosting_operations o JOIN hosting_binding_state bs ON bs.binding_id=o.binding_id JOIN hosting_apply_plans p ON p.operation_id=bs.healthy->>'operation_id' WHERE o.id=NEW.operation_id AND o.id<>bs.healthy->>'operation_id' AND p.body->>'compiler_version'='database-hosting.private.membership.v2' AND EXISTS(SELECT 1 FROM hosting_database_deployments d WHERE d.operation_id=o.id)) THEN RAISE EXCEPTION 'database v1 downgrade denied' USING ERRCODE='23514'; END IF;
 RETURN NULL;
END $$;


CREATE FUNCTION nerocd_database_connection_request_validate() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE b hosting_database_bindings; receipt hosting_database_binding_receipts; expected_capture text;
BEGIN
 SELECT * INTO STRICT b FROM hosting_database_bindings WHERE id=NEW.binding_id;
 SELECT * INTO STRICT receipt FROM hosting_database_binding_receipts WHERE application_resource_id=NEW.application_resource_id AND request_id=NEW.accepted_request->'binding'->>'request_id';
 expected_capture:='hnc_'||left(encode(sha256(convert_to('{"Source":'||to_jsonb(NEW.source_operation_id)::text||'}','UTF8')),'hex'),32);
 IF b.application_resource_id<>NEW.application_resource_id OR b.database_resource_id<>NEW.database_resource_id OR b.project_id<>NEW.project_id OR b.database_operation_id<>NEW.source_operation_id OR receipt.actor_id<>NEW.created_by OR receipt.accepted_request IS DISTINCT FROM NEW.accepted_request->'binding' OR NEW.capture_id<>expected_capture OR NEW.binding_application_snapshot->>'policy_version'<>'public-image.runtime-env.release-ports.v1' OR NEW.binding_application_snapshot ? 'application_database' OR NEW.binding_application_snapshot->'runtime_configuration' IS DISTINCT FROM b.body->'runtime_configuration' OR NEW.binding_application_snapshot->>'resource_id'<>b.application_resource_id OR NEW.binding_application_snapshot->>'binding_id'<>b.application_binding_id OR NEW.binding_application_snapshot->>'destination_id'<>b.destination_id OR NEW.binding_application_snapshot->>'server_id' IS DISTINCT FROM b.body->>'server_id' OR NEW.binding_application_snapshot->>'runner_id' IS DISTINCT FROM b.body->>'runner_id' OR NEW.accepted_request->'deployment'->>'expected_generation' IS DISTINCT FROM NEW.accepted_request->'binding'->>'expected_resource_generation' OR coalesce(NEW.accepted_request->'deployment'->>'expected_runtime_configuration_generation','0') IS DISTINCT FROM NEW.accepted_request->'binding'->>'expected_runtime_generation' OR NEW.accepted_request->'deployment' ? 'storage_route_continuity' OR NEW.state<>'binding_saved' OR NEW.reason_code<>'accepted' THEN RAISE EXCEPTION 'connection request immutable binding association mismatch' USING ERRCODE='23514'; END IF;
 IF NOT EXISTS(SELECT 1 FROM audit_events a WHERE a.id=NEW.audit_id AND a.actor_id=NEW.created_by AND a.target_id=NEW.id AND a.action='hosting.database_connection.requested' AND a.metadata->>'binding_id'=b.id AND a.metadata->>'binding_hash'=b.body_hash AND a.metadata->>'request_hash'=NEW.request_hash) OR NOT EXISTS(SELECT 1 FROM hosting_database_network_holds h WHERE h.connection_id=NEW.id AND h.database_binding_id=b.database_binding_id AND h.operation_id=NEW.upgrade_operation_id) OR NOT EXISTS(SELECT 1 FROM river_job j WHERE j.kind='hosting.database_connection.v1' AND j.args->>'connection_id'=NEW.id) THEN RAISE EXCEPTION 'connection request requires atomic audit hold and River intent' USING ERRCODE='23514'; END IF;
 RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER hosting_connection_request_consistency AFTER INSERT ON hosting_database_connections DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION nerocd_database_connection_request_validate();

DO $$ DECLARE owner_schema text:=current_schema(); routine text; BEGIN
 FOREACH routine IN ARRAY ARRAY['nerocd_database_connection_transition_retired(jsonb,jsonb,jsonb)','nerocd_database_connection_defer_admission()','nerocd_database_connection_effect_validate()','nerocd_database_connection_request_validate()','nerocd_database_connection_identity()','nerocd_database_connection_flush()','nerocd_database_connection_validate(text,boolean)','nerocd_database_connection_consistent()','nerocd_database_binding_pending_effect()','nerocd_database_connection_hold_guard()','nerocd_database_connection_stage_validate()','nerocd_database_connection_schema_compatible()','nerocd_database_connection_lease_guard()','nerocd_database_connection_retirement_validate()','nerocd_database_membership_advance()','nerocd_database_connection_hold_release()','nerocd_database_network_upgrade_consistent()'] LOOP EXECUTE format('ALTER FUNCTION %I.%s SET search_path=%I,pg_catalog,pg_temp',owner_schema,routine,owner_schema); END LOOP;
 -- Preserve the catalog-first contract checked by build deployment readiness.
 EXECUTE format('ALTER FUNCTION %I.nerocd_hosting_build_deployment_consistent() SET search_path TO pg_catalog, %I, pg_temp',owner_schema,owner_schema);
END $$;
