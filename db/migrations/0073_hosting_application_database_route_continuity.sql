-- A ready connected image yields its original lease before a strict proxy task.
-- Child identities never replace hosting_operations.run_id or observation identity.
ALTER TABLE hosting_task_targets DROP CONSTRAINT hosting_task_targets_mode_check;
ALTER TABLE hosting_task_targets ADD CONSTRAINT hosting_task_targets_mode_check CHECK(mode IN ('deploy','observe','cleanup','finalize'));
CREATE UNIQUE INDEX hosting_one_finalize_task ON hosting_task_targets(operation_id) WHERE mode='finalize';

CREATE TABLE hosting_application_database_route_continuations (
 operation_id text PRIMARY KEY REFERENCES hosting_database_connection_executions(operation_id) ON DELETE RESTRICT,
 proxy_id text NOT NULL REFERENCES hosting_proxies(id) ON DELETE RESTRICT,
 proxy_operation_id text NOT NULL UNIQUE CHECK(proxy_operation_id='appdb_proxy_'||operation_id),
 finalize_run_id text NOT NULL UNIQUE CHECK(finalize_run_id='finalize_'||operation_id),
 body jsonb NOT NULL CHECK(jsonb_typeof(body)='object' AND pg_column_size(body)<=32768),
 body_hash text NOT NULL CHECK(body_hash ~ '^sha256:[0-9a-f]{64}$'),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 CHECK((body->>'version'='application-database.route-continuity.v1' AND body->>'operation_id'=operation_id AND body->>'proxy_id'=proxy_id AND body->>'proxy_operation_id'=proxy_operation_id AND body->>'finalize_run_id'=finalize_run_id AND body->>'hash'=body_hash AND body - 'version' - 'operation_id' - 'proxy_id' - 'proxy_operation_id' - 'finalize_run_id' - 'request' - 'loaded_snapshot_hash' - 'previous_healthy' - 'hash'='{}'::jsonb) IS TRUE)
);
CREATE TRIGGER hosting_appdb_route_continuations_immutable BEFORE UPDATE OR DELETE ON hosting_application_database_route_continuations FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_execution_immutable();

CREATE TABLE hosting_application_database_yields (
 operation_id text PRIMARY KEY REFERENCES hosting_application_database_route_continuations(operation_id) ON DELETE RESTRICT,
 receipt_id text NOT NULL UNIQUE CHECK(length(receipt_id) BETWEEN 1 AND 128),
 request jsonb NOT NULL CHECK(jsonb_typeof(request)='object' AND pg_column_size(request)<=16384),
 request_hash text NOT NULL CHECK(request_hash ~ '^sha256:[0-9a-f]{64}$'),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 CHECK((request->>'version'='application-database.route-continuity.v1' AND request->>'operation_id'=operation_id AND request->>'receipt_id'=receipt_id AND request - 'version' - 'operation_id' - 'run_id' - 'lease_id' - 'attempt' - 'fence' - 'receipt_id' - 'plan_hash' - 'stage_hash' - 'candidate_hash' - 'continuation_hash'='{}'::jsonb) IS TRUE)
);
CREATE TRIGGER hosting_appdb_yields_immutable BEFORE UPDATE OR DELETE ON hosting_application_database_yields FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_execution_immutable();

CREATE FUNCTION nerocd_appdb_route_reservation_validate() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE e hosting_database_connection_executions; o hosting_operations; p hosting_proxies; prior hosting_proxy_operations; pick jsonb; count_routes integer;
BEGIN
 SELECT * INTO STRICT e FROM hosting_database_connection_executions WHERE operation_id=NEW.operation_id AND family='application';
 SELECT * INTO STRICT o FROM hosting_operations WHERE id=e.operation_id;
 SELECT * INTO STRICT p FROM hosting_proxies WHERE id=NEW.proxy_id;
 SELECT * INTO STRICT prior FROM hosting_proxy_operations WHERE id=NEW.body->'request'->>'expected_loaded_proxy_operation_id' AND proxy_id=p.id AND state='applied';
 IF o.state<>'queued' OR o.run_id IS NOT NULL OR jsonb_array_length(e.proof->'members')<>1 OR NEW.body->'previous_healthy' IS DISTINCT FROM e.proof->'members'->0->'application_healthy' OR NEW.body->'previous_healthy' IS DISTINCT FROM (SELECT healthy FROM hosting_binding_state WHERE binding_id=o.binding_id) OR prior.generation<>p.loaded_generation OR prior.generation::text IS DISTINCT FROM NEW.body->'request'->>'expected_loaded_generation' OR NOT p.enabled OR EXISTS(SELECT 1 FROM hosting_proxy_restore_holds WHERE proxy_id=p.id) OR p.active_operation_id IS NOT NULL OR p.hold_operation_id IS NOT NULL OR prior.snapshot IS NULL OR NEW.body->>'loaded_snapshot_hash' IS DISTINCT FROM prior.snapshot_hash OR p.daemon_id IS DISTINCT FROM NEW.body->'previous_healthy'->'release'->>'daemon_id' OR prior.snapshot->>'destination_id' IS DISTINCT FROM (SELECT body->>'destination_id' FROM hosting_snapshots WHERE operation_id=o.id) THEN RAISE EXCEPTION 'invalid connected route reservation' USING ERRCODE='23514'; END IF;
 IF jsonb_typeof(NEW.body->'request'->'routes') IS DISTINCT FROM 'array' OR jsonb_array_length(NEW.body->'request'->'routes') NOT BETWEEN 1 AND 32 OR (NEW.body->'request') - 'expected_loaded_generation' - 'expected_loaded_proxy_operation_id' - 'routes'<>'{}'::jsonb THEN RAISE EXCEPTION 'invalid connected route scope' USING ERRCODE='23514'; END IF;
 SELECT count(*) INTO count_routes FROM hosting_proxy_routes WHERE proxy_id=p.id AND resource_id=o.resource_id AND enabled;
 IF count_routes<>jsonb_array_length(NEW.body->'request'->'routes') OR count_routes<>(SELECT count(DISTINCT value->>'route_id') FROM jsonb_array_elements(NEW.body->'request'->'routes')) THEN RAISE EXCEPTION 'connected route reservation must cover every enabled application route' USING ERRCODE='23514'; END IF;
 FOR pick IN SELECT value FROM jsonb_array_elements(NEW.body->'request'->'routes') LOOP
 IF pick - 'route_id' - 'expected_version' - 'expected_selected_release_operation_id'<>'{}'::jsonb OR NOT EXISTS(SELECT 1 FROM hosting_proxy_routes r WHERE r.id=pick->>'route_id' AND r.proxy_id=p.id AND r.resource_id=o.resource_id AND r.binding_id=o.binding_id AND r.project_id=o.project_id AND r.enabled AND r.version::text=pick->>'expected_version' AND r.selected_release_operation_id=pick->>'expected_selected_release_operation_id' AND r.selected_release_operation_id=NEW.body->'previous_healthy'->>'operation_id') THEN RAISE EXCEPTION 'connected route selection mismatch' USING ERRCODE='23514'; END IF;
 END LOOP;
 RETURN NEW;
END $$;
CREATE TRIGGER hosting_appdb_route_reservation BEFORE INSERT ON hosting_application_database_route_continuations FOR EACH ROW EXECUTE FUNCTION nerocd_appdb_route_reservation_validate();

CREATE FUNCTION nerocd_appdb_yield_validate() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE c hosting_application_database_route_continuations; o hosting_operations; stage hosting_application_database_stages; lease run_leases; task hosting_task_targets;
BEGIN
 SELECT * INTO STRICT c FROM hosting_application_database_route_continuations WHERE operation_id=NEW.operation_id;
 SELECT * INTO STRICT o FROM hosting_operations WHERE id=NEW.operation_id AND state IN ('applying','verifying');
 SELECT * INTO STRICT stage FROM hosting_application_database_stages WHERE operation_id=o.id AND phase='ready';
 SELECT * INTO STRICT lease FROM run_leases WHERE id=NEW.request->>'lease_id';
 SELECT * INTO STRICT task FROM hosting_task_targets WHERE run_id=lease.run_id;
 IF task.operation_id<>o.id OR task.mode<>'deploy' OR lease.status<>'active' OR lease.expires_at<=clock_timestamp() OR lease.run_id<>o.run_id OR lease.run_id IS DISTINCT FROM NEW.request->>'run_id' OR lease.attempt::text IS DISTINCT FROM NEW.request->>'attempt' OR lease.fence IS DISTINCT FROM NEW.request->>'fence' OR NEW.request->>'continuation_hash' IS DISTINCT FROM c.body_hash OR NEW.request->>'stage_hash' IS DISTINCT FROM stage.stage_hash OR NEW.request->>'candidate_hash' IS DISTINCT FROM stage.stage->'candidate'->>'hash' OR NEW.request->>'plan_hash' IS DISTINCT FROM stage.stage->>'plan_hash' OR NOT EXISTS(SELECT 1 FROM hosting_binding_state WHERE binding_id=o.binding_id AND hold_operation_id=o.id AND healthy=c.body->'previous_healthy') THEN RAISE EXCEPTION 'connected prepare yield authority mismatch' USING ERRCODE='23514'; END IF;
 PERFORM nerocd_database_connection_validate(o.id,true);
 RETURN NEW;
END $$;
CREATE TRIGGER hosting_appdb_yield_authority BEFORE INSERT ON hosting_application_database_yields FOR EACH ROW EXECUTE FUNCTION nerocd_appdb_yield_validate();

-- Receipt, original task completion and durable continuation intent commit together.
CREATE FUNCTION nerocd_appdb_yield_settlement() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NOT EXISTS(SELECT 1 FROM run_leases l JOIN task_runs t ON t.id=l.run_id WHERE l.id=NEW.request->>'lease_id' AND l.status='succeeded' AND l.completion_key=NEW.receipt_id AND t.status='succeeded') OR NOT EXISTS(SELECT 1 FROM river_job WHERE kind='hosting.application_database_route.v1' AND args->>'operation_id'=NEW.operation_id) THEN RAISE EXCEPTION 'connected yield requires prepare settlement and continuation intent' USING ERRCODE='23514'; END IF;
 RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER hosting_appdb_yield_settlement AFTER INSERT ON hosting_application_database_yields DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION nerocd_appdb_yield_settlement();

-- This predicate is association evidence only; Go additionally checks the whole
-- strict HTTPS result, current grants/owner/secret liveness and placement.
CREATE FUNCTION nerocd_appdb_route_applied(operation_key text) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT EXISTS(SELECT 1 FROM hosting_application_database_route_continuations c
 JOIN hosting_application_database_yields y ON y.operation_id=c.operation_id
 JOIN hosting_proxy_operations p ON p.id=c.proxy_operation_id AND p.proxy_id=c.proxy_id
 JOIN hosting_proxy_receipts receipt ON receipt.operation_id=p.id AND receipt.phase='complete'
 JOIN hosting_proxies proxy ON proxy.id=p.proxy_id
 WHERE c.operation_id=operation_key AND p.state='applied' AND NOT EXISTS(SELECT 1 FROM audit_events WHERE target_id=p.id AND action='hosting.route_switch_metadata_conflict')
 AND receipt.body->>'outcome'='applied' AND receipt.body->>'plan_hash'=p.plan_hash
 AND receipt.body->>'loaded_generation'=p.generation::text AND proxy.loaded_generation=p.generation
 AND NOT EXISTS(SELECT 1 FROM hosting_proxy_restore_holds WHERE proxy_id=proxy.id) AND proxy.hold_operation_id IS NULL AND proxy.active_operation_id IS NULL
 AND p.snapshot->'verified_switch'->>'prior_applied_proxy_operation_id'=c.body->'request'->>'expected_loaded_proxy_operation_id'
 AND p.snapshot->'verified_switch'->>'expected_loaded_generation'=c.body->'request'->>'expected_loaded_generation');
$$;

CREATE FUNCTION nerocd_appdb_finalize_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE target hosting_task_targets; c hosting_application_database_route_continuations; o hosting_operations; task task_runs; runner runners;
BEGIN
 IF TG_TABLE_NAME='hosting_task_targets' THEN target:=NEW;
 ELSE SELECT * INTO target FROM hosting_task_targets WHERE run_id=NEW.run_id; END IF;
 IF target.mode IS DISTINCT FROM 'finalize' THEN RETURN NEW; END IF;
 SELECT * INTO STRICT c FROM hosting_application_database_route_continuations WHERE operation_id=target.operation_id AND finalize_run_id=target.run_id;
 SELECT * INTO STRICT o FROM hosting_operations WHERE id=c.operation_id AND state IN('applying','verifying');
 SELECT * INTO STRICT task FROM task_runs WHERE id=target.run_id;
 SELECT * INTO STRICT runner FROM runners WHERE id=target.runner_id FOR SHARE;
 IF NOT nerocd_appdb_route_applied(o.id) OR target.binding_id<>o.binding_id OR runner.id IS DISTINCT FROM (SELECT body->>'runner_id' FROM hosting_snapshots WHERE operation_id=o.id) OR NOT ('hosting_application_database_route_continuity_v1'=ANY(runner.capabilities) AND 'hosting_proxy_verified_switch_v1'=ANY(runner.capabilities)) OR task.project_id<>o.project_id OR task.requested_by<>o.created_by OR task.run_spec IS DISTINCT FROM jsonb_build_object('type','image_deploy','hosting_operation_id',o.id,'hosting_mode','finalize','inputs','{}'::jsonb) OR task.workflow<>'{}'::jsonb OR NOT EXISTS(SELECT 1 FROM hosting_binding_state WHERE binding_id=o.binding_id AND hold_operation_id=o.id AND healthy=c.body->'previous_healthy') THEN RAISE EXCEPTION 'connected finalize task lacks exact applied proof' USING ERRCODE='23514'; END IF;
 IF TG_TABLE_NAME='run_leases' THEN
 IF NEW.runner_id<>target.runner_id OR NEW.attempt<>1 THEN RAISE EXCEPTION 'connected finalize cannot invent retry authority' USING ERRCODE='23514'; END IF;
 END IF;
 PERFORM nerocd_database_connection_validate(o.id,true);
 RETURN NEW;
END $$;
CREATE TRIGGER hosting_appdb_finalize_target BEFORE INSERT ON hosting_task_targets FOR EACH ROW EXECUTE FUNCTION nerocd_appdb_finalize_guard();
CREATE TRIGGER hosting_appdb_finalize_lease BEFORE INSERT ON run_leases FOR EACH ROW EXECUTE FUNCTION nerocd_appdb_finalize_guard();

CREATE FUNCTION nerocd_appdb_route_effect_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE c hosting_application_database_route_continuations; run_key text; action_key text; lease_key text; lease run_leases;
BEGIN
 SELECT * INTO c FROM hosting_application_database_route_continuations WHERE operation_id=NEW.operation_id;
 IF NOT FOUND THEN RETURN NEW; END IF;
 IF TG_TABLE_NAME='hosting_application_database_effect_authorizations' THEN run_key:=NEW.request->>'run_id'; action_key:=NEW.request->>'action'; lease_key:=NEW.request->>'lease_id';
 ELSE run_key:=NEW.authority->>'run_id'; action_key:='retire'; lease_key:=NEW.authority->>'lease_id'; END IF;
 IF action_key='retire' THEN
 SELECT * INTO STRICT lease FROM run_leases WHERE id=lease_key;
 IF run_key<>c.finalize_run_id OR lease.run_id<>run_key OR lease.status<>'active' OR lease.expires_at<=clock_timestamp() OR NOT nerocd_appdb_route_applied(c.operation_id) THEN RAISE EXCEPTION 'connected retirement requires fresh finalize lease and applied switch' USING ERRCODE='23514'; END IF;
 ELSIF run_key<>'run_'||c.operation_id OR EXISTS(SELECT 1 FROM hosting_application_database_yields WHERE operation_id=c.operation_id) THEN RAISE EXCEPTION 'yielded prepare cannot mutate' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER hosting_appdb_effect_mode BEFORE INSERT ON hosting_application_database_effect_authorizations FOR EACH ROW EXECUTE FUNCTION nerocd_appdb_route_effect_guard();
CREATE TRIGGER hosting_appdb_retirement_mode BEFORE INSERT ON hosting_application_database_retirements FOR EACH ROW EXECUTE FUNCTION nerocd_appdb_route_effect_guard();

CREATE FUNCTION nerocd_appdb_route_complete_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE c hosting_application_database_route_continuations;
BEGIN
 IF NEW.phase<>'complete' THEN RETURN NEW; END IF;
 SELECT * INTO c FROM hosting_application_database_route_continuations WHERE operation_id=NEW.operation_id;
 IF NOT FOUND THEN RETURN NEW; END IF;
 IF NEW.authority->>'run_id' IS DISTINCT FROM c.finalize_run_id OR NOT nerocd_appdb_route_applied(c.operation_id) THEN RAISE EXCEPTION 'connected completion requires exact finalize task' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER hosting_appdb_complete_mode BEFORE INSERT ON hosting_receipts FOR EACH ROW EXECUTE FUNCTION nerocd_appdb_route_complete_guard();

CREATE OR REPLACE FUNCTION nerocd_database_connection_effect_validate() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE e hosting_database_connection_executions; o hosting_operations; lease run_leases; latest hosting_application_database_stages; p jsonb; b hosting_database_bindings;
BEGIN
 SELECT * INTO STRICT e FROM hosting_database_connection_executions WHERE operation_id=NEW.operation_id AND family='application';
 SELECT * INTO STRICT o FROM hosting_operations WHERE id=e.operation_id AND state IN('applying','verifying');
 SELECT body INTO STRICT p FROM hosting_apply_plans WHERE operation_id=o.id;
 SELECT * INTO STRICT b FROM hosting_database_bindings WHERE id=e.proof->'binding'->>'id';
 SELECT * INTO STRICT lease FROM run_leases WHERE id=NEW.request->>'lease_id' FOR SHARE;
 SELECT * INTO latest FROM hosting_application_database_stages WHERE operation_id=o.id ORDER BY created_at DESC,receipt_id DESC LIMIT 1;
 IF lease.status<>'active' OR lease.expires_at<=clock_timestamp() OR (lease.run_id<>o.run_id AND NOT EXISTS(SELECT 1 FROM hosting_application_database_route_continuations c WHERE c.operation_id=o.id AND c.finalize_run_id=lease.run_id AND NEW.request->>'action'='retire' AND nerocd_appdb_route_applied(o.id))) OR lease.run_id IS DISTINCT FROM NEW.request->>'run_id' OR lease.runner_id IS DISTINCT FROM p->'snapshot'->>'runner_id' OR lease.attempt::text IS DISTINCT FROM NEW.request->>'attempt' OR lease.fence IS DISTINCT FROM NEW.request->>'fence' OR NEW.response->>'plan_hash' IS DISTINCT FROM p->>'hash' OR NEW.response->>'membership_revision' IS DISTINCT FROM e.expected_membership_revision::text OR (NEW.response->>'use_until')::timestamptz IS DISTINCT FROM NEW.use_until OR NEW.use_until>least(lease.expires_at,clock_timestamp()+interval '30 seconds') OR NEW.use_until<=clock_timestamp() OR NOT EXISTS(SELECT 1 FROM hosting_database_network_holds h WHERE h.operation_id=o.id AND h.database_binding_id=b.database_binding_id) OR NOT EXISTS(SELECT 1 FROM hosting_binding_state state WHERE state.binding_id=o.binding_id AND state.hold_operation_id=o.id) THEN RAISE EXCEPTION 'connection effect authority mismatch' USING ERRCODE='23514'; END IF;
 IF NOT ((NEW.request->>'action'='create' AND latest.operation_id IS NULL AND NEW.request->>'expected_stage_hash'='') OR (NEW.request->>'expected_stage_hash'=latest.stage_hash AND ((NEW.request->>'action'='configure' AND latest.phase='attach_intent') OR (NEW.request->>'action'='start' AND latest.phase='network_configured') OR (NEW.request->>'action'='retire' AND latest.phase='ready' AND EXISTS(SELECT 1 FROM hosting_application_database_retirements r WHERE r.operation_id=o.id AND r.phase='retire_intent') AND NOT EXISTS(SELECT 1 FROM hosting_application_database_retirements r WHERE r.operation_id=o.id AND r.phase='detached'))))) THEN RAISE EXCEPTION 'connection effect does not match current stage' USING ERRCODE='23514'; END IF;
 PERFORM nerocd_database_connection_validate(o.id,true);
 RETURN NEW;
END $$;


-- Only the reserved proxy operation can target a ready, still-held candidate.
-- Freeze unrelated routes and the exact predecessor descriptor in the database.
CREATE FUNCTION nerocd_appdb_proxy_association() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE c hosting_application_database_route_continuations; o hosting_operations; prior hosting_proxy_operations; snap jsonb; expected_routes jsonb; expected_selected jsonb;
BEGIN
 SELECT * INTO c FROM hosting_application_database_route_continuations WHERE proxy_operation_id=NEW.id;
 IF NOT FOUND THEN
  IF EXISTS(SELECT 1 FROM jsonb_array_elements(NEW.snapshot->'routes') r JOIN hosting_application_database_route_continuations continuation ON continuation.operation_id=r->>'release_operation_id' JOIN hosting_operations candidate ON candidate.id=continuation.operation_id WHERE candidate.state<>'succeeded') THEN RAISE EXCEPTION 'held connected candidate requires exact reserved proxy operation' USING ERRCODE='23514'; END IF;
  RETURN NULL;
 END IF;
 SELECT * INTO STRICT o FROM hosting_operations WHERE id=c.operation_id AND state IN('applying','verifying');
 SELECT * INTO STRICT prior FROM hosting_proxy_operations WHERE id=c.body->'request'->>'expected_loaded_proxy_operation_id' AND proxy_id=c.proxy_id AND state='applied';
 SELECT body INTO STRICT snap FROM hosting_snapshots WHERE operation_id=o.id;
 IF NOT EXISTS(SELECT 1 FROM hosting_application_database_yields WHERE operation_id=o.id) OR NEW.proxy_id<>c.proxy_id OR NEW.created_by<>o.created_by OR NEW.project_id IS DISTINCT FROM o.project_id OR NEW.snapshot - 'generation' - 'routes' - 'verified_switch' IS DISTINCT FROM prior.snapshot - 'generation' - 'routes' - 'verified_switch' THEN RAISE EXCEPTION 'connected proxy association mismatch' USING ERRCODE='23514'; END IF;
 SELECT jsonb_agg(CASE WHEN r->>'resource_id'=o.resource_id THEN jsonb_set(jsonb_set(r,'{release_operation_id}',to_jsonb(o.id)),'{host_port}',snap->'host_port') ELSE r END ORDER BY r->>'route_id') INTO expected_routes FROM jsonb_array_elements(prior.snapshot->'routes') r;
 SELECT jsonb_agg(jsonb_build_object('route_id',pick->>'route_id','prior_version',pick->'expected_version','prior_target',r) ORDER BY pick->>'route_id') INTO expected_selected FROM jsonb_array_elements(c.body->'request'->'routes') pick JOIN jsonb_array_elements(prior.snapshot->'routes') r ON r->>'route_id'=pick->>'route_id';
 IF NEW.snapshot->'routes' IS DISTINCT FROM expected_routes OR NEW.snapshot->'verified_switch' IS DISTINCT FROM jsonb_build_object('version',1,'expected_loaded_generation',c.body->'request'->'expected_loaded_generation','prior_applied_proxy_operation_id',prior.id,'routes',expected_selected) THEN RAISE EXCEPTION 'connected switch changed frozen route or predecessor scope' USING ERRCODE='23514'; END IF;
 RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER hosting_appdb_proxy_association AFTER INSERT ON hosting_proxy_operations DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION nerocd_appdb_proxy_association();

CREATE FUNCTION nerocd_appdb_yield_defer() RETURNS void LANGUAGE plpgsql AS $$ BEGIN SET CONSTRAINTS hosting_appdb_yield_settlement DEFERRED; END $$;

CREATE FUNCTION nerocd_appdb_route_schema_compatible() RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT NOT EXISTS(SELECT 1 FROM (VALUES
 ('hosting_application_database_route_continuations','hosting_appdb_route_continuations_immutable'),
 ('hosting_application_database_route_continuations','hosting_appdb_route_reservation'),
 ('hosting_application_database_yields','hosting_appdb_yields_immutable'),
 ('hosting_application_database_yields','hosting_appdb_yield_authority'),
 ('hosting_application_database_yields','hosting_appdb_yield_settlement'),
 ('hosting_task_targets','hosting_appdb_finalize_target'),('run_leases','hosting_appdb_finalize_lease'),
 ('hosting_application_database_effect_authorizations','hosting_appdb_effect_mode'),
 ('hosting_application_database_retirements','hosting_appdb_retirement_mode'),
 ('hosting_receipts','hosting_appdb_complete_mode'),('hosting_proxy_operations','hosting_appdb_proxy_association')) required(table_name,trigger_name)
 WHERE NOT EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass(current_schema()||'.'||table_name) AND tgname=trigger_name AND tgenabled IN('O','A')));
$$;
DO $$ DECLARE owner_schema text:=current_schema(); routine text; BEGIN
 FOREACH routine IN ARRAY ARRAY['nerocd_appdb_proxy_association()','nerocd_appdb_yield_defer()','nerocd_appdb_route_reservation_validate()','nerocd_appdb_yield_validate()','nerocd_appdb_yield_settlement()','nerocd_appdb_route_applied(text)','nerocd_appdb_finalize_guard()','nerocd_appdb_route_effect_guard()','nerocd_appdb_route_complete_guard()','nerocd_database_connection_effect_validate()','nerocd_appdb_route_schema_compatible()'] LOOP EXECUTE format('ALTER FUNCTION %I.%s SET search_path=%I,pg_catalog,pg_temp',owner_schema,routine,owner_schema); END LOOP;
END $$;
