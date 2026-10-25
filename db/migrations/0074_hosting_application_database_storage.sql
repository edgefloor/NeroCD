-- Explicit composition; prior storage-less connected plans remain closed.
CREATE TABLE hosting_application_database_storage (
 operation_id text PRIMARY KEY REFERENCES hosting_database_connection_executions(operation_id) ON DELETE RESTRICT,
 storage_id text NOT NULL REFERENCES hosting_storage(id) ON DELETE RESTRICT,
 body jsonb NOT NULL CHECK(jsonb_typeof(body)='object' AND pg_column_size(body)<=4096),
 body_hash text NOT NULL CHECK(body_hash ~ '^sha256:[0-9a-f]{64}$'),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 CHECK((body->>'version'='application-database.storage.v1' AND body->>'strategy'='replacement_with_interruption' AND body->'storage_ref'->>'storage_id'=storage_id AND body->>'hash'=body_hash AND body-'version'-'strategy'-'storage_ref'-'hash'='{}'::jsonb) IS TRUE)
);
CREATE TRIGGER hosting_appdb_storage_immutable BEFORE UPDATE OR DELETE ON hosting_application_database_storage FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_execution_immutable();

CREATE TABLE hosting_application_database_storage_writers (
 operation_id text NOT NULL REFERENCES hosting_application_database_storage(operation_id) ON DELETE RESTRICT,
 phase text NOT NULL CHECK(phase IN ('stop_intent','stopped','candidate_stopped','replacement_failed','resume_intent','resumed')),
 receipt_id text NOT NULL UNIQUE,
 request jsonb NOT NULL CHECK(jsonb_typeof(request)='object' AND pg_column_size(request)<=32768),
 request_hash text NOT NULL CHECK(request_hash ~ '^sha256:[0-9a-f]{64}$'),
 response jsonb NOT NULL CHECK(jsonb_typeof(response)='object' AND pg_column_size(response)<=262144),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(operation_id,phase),
 CHECK((request->>'operation_id'=operation_id AND request->'receipt'->>'phase'=phase AND request->'receipt'->>'receipt_id'=receipt_id AND request->'receipt'->>'version'='application-database.storage.v1') IS TRUE)
);
CREATE TRIGGER hosting_appdb_storage_writers_immutable BEFORE UPDATE OR DELETE ON hosting_application_database_storage_writers FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_execution_immutable();

CREATE TABLE hosting_application_database_storage_recoveries (
 operation_id text PRIMARY KEY REFERENCES hosting_application_database_storage(operation_id) ON DELETE RESTRICT,
 action text NOT NULL CHECK(action IN ('resume_predecessor','complete_replacement')),
 run_id text NOT NULL UNIQUE,
 proxy_operation_id text UNIQUE,
 actor_id text NOT NULL REFERENCES principals(id),
 request jsonb NOT NULL CHECK(jsonb_typeof(request)='object' AND pg_column_size(request)<=16384),
 request_hash text NOT NULL CHECK(request_hash ~ '^sha256:[0-9a-f]{64}$'),
 body jsonb NOT NULL CHECK(jsonb_typeof(body)='object' AND pg_column_size(body)<=196608),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 CHECK((action='resume_predecessor' AND proxy_operation_id IS NULL AND run_id='storage_recover_'||operation_id) OR (action='complete_replacement' AND proxy_operation_id='appdb_storage_proxy_'||operation_id AND run_id='proxy_'||proxy_operation_id))
);
CREATE TRIGGER hosting_appdb_storage_recoveries_immutable BEFORE UPDATE OR DELETE ON hosting_application_database_storage_recoveries FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_execution_immutable();

CREATE OR REPLACE FUNCTION nerocd_database_connection_validate(operation_key text, admission boolean) RETURNS void LANGUAGE plpgsql AS $$
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
  IF o.resource_id<>c.application_resource_id OR o.binding_id<>b.application_binding_id OR o.project_id<>c.project_id OR e.proof->'binding' IS DISTINCT FROM b.body OR e.proof->'binding_database_plan' IS DISTINCT FROM original_plan OR e.proof->'binding_application_snapshot' IS DISTINCT FROM c.binding_application_snapshot OR e.proof->'origin' IS DISTINCT FROM origin->'origin' OR snap->>'policy_version' NOT IN ('public-image.runtime-env.database.release-ports.v1','public-image.application-database.owned-storage.v1') OR snap->'application_database' IS DISTINCT FROM jsonb_build_object('binding_id',b.id,'binding_hash',b.body_hash) OR snap->'runtime_configuration' IS DISTINCT FROM b.body->'runtime_configuration' OR snap ?| ARRAY['database','storage_route_continuity','local_build'] OR (snap ? 'storage') IS DISTINCT FROM (snap->>'policy_version'='public-image.application-database.owned-storage.v1') OR e.proof - 'version' - 'operation_id' - 'create_nonce' - 'binding' - 'application_project_id' - 'database_project_id' - 'binding_application_snapshot' - 'binding_database_plan' - 'database_plan' - 'database_healthy' - 'origin' - 'membership_revision' - 'members' - 'retired' - 'hash'<>'{}'::jsonb OR e.proof->>'application_project_id'<>c.project_id OR e.proof->>'database_project_id'<>c.project_id THEN RAISE EXCEPTION 'application connection proof or family mismatch' USING ERRCODE='23514'; END IF;
 ELSE
  db_plan:=e.proof->'previous_plan'; db_healthy:=e.proof->'previous_healthy';
  IF o.resource_id<>c.database_resource_id OR o.binding_id<>b.database_binding_id OR o.project_id<>c.project_id OR e.proof->'capture_plan' IS DISTINCT FROM captured OR e.proof->'origin_receipt' IS DISTINCT FROM origin OR e.proof - 'version' - 'capture_plan' - 'origin_receipt' - 'previous_plan' - 'previous_healthy' - 'membership_revision' - 'members' - 'retired' - 'hash'<>'{}'::jsonb OR snap IS DISTINCT FROM db_plan->'snapshot' OR EXISTS(SELECT 1 FROM hosting_database_network_upgrades WHERE operation_id=o.id) THEN RAISE EXCEPTION 'database maintenance proof or family mismatch' USING ERRCODE='23514'; END IF;
 END IF;
 IF jsonb_typeof(e.proof->'members') IS DISTINCT FROM 'array' OR jsonb_array_length(e.proof->'members')>1 OR jsonb_typeof(e.proof->'retired') IS DISTINCT FROM 'array' OR db_plan->>'compiler_version'<>'database-hosting.private.membership.v2' OR db_plan->'database'->'network'->>'network_id' IS DISTINCT FROM origin->'origin'->>'network_id' OR db_plan->'database'->'network'->>'origin_hash' IS DISTINCT FROM origin->>'origin_hash' OR NOT EXISTS(SELECT 1 FROM hosting_apply_plans p JOIN hosting_operations prior ON prior.id=p.operation_id JOIN hosting_receipts receipt ON receipt.operation_id=prior.id AND receipt.phase='complete' WHERE prior.id=db_healthy->>'operation_id' AND prior.state='succeeded' AND prior.resource_id=c.database_resource_id AND p.body=db_plan AND p.body_hash=db_healthy->>'plan_hash' AND receipt.body->>'container_id'=db_healthy->>'container_id' AND receipt.body->>'ready'='true') THEN RAISE EXCEPTION 'connection database predecessor association mismatch' USING ERRCODE='23514'; END IF;
 IF NOT EXISTS(SELECT 1 FROM audit_events a WHERE a.id=e.audit_id AND a.target_id=o.id AND a.actor_id=o.created_by AND a.metadata->>'proof_hash'=e.proof_hash AND a.action=CASE e.family WHEN 'application' THEN 'hosting.application_database.requested' ELSE 'hosting.database_maintenance.requested' END) THEN RAISE EXCEPTION 'connection execution audit mismatch' USING ERRCODE='23514'; END IF;
 IF e.family='application' THEN
  IF snap ? 'storage' THEN
   IF NOT EXISTS(SELECT 1 FROM hosting_application_database_storage st JOIN hosting_storage owned ON owned.id=st.storage_id JOIN runners runner ON runner.id=snap->>'runner_id' WHERE st.operation_id=o.id AND st.body->'storage_ref'=snap->'storage' AND owned.resource_id=o.resource_id AND owned.binding_id=o.binding_id AND owned.project_id=o.project_id AND owned.destination_id=snap->>'destination_id' AND owned.generation::text=snap->'storage'->>'generation' AND owned.body_hash=snap->'storage'->>'hash' AND (NOT admission OR 'hosting_application_database_storage_v1'=ANY(runner.capabilities))) OR jsonb_array_length(e.proof->'members')<>1 THEN RAISE EXCEPTION 'connected storage requires exact owned composition and interruption' USING ERRCODE='23514'; END IF;
  ELSIF EXISTS(SELECT 1 FROM hosting_application_database_storage WHERE operation_id=o.id) THEN RAISE EXCEPTION 'storage composition cannot bind old policy' USING ERRCODE='23514'; END IF;
 END IF;
 IF admission AND (head.revision<>e.expected_membership_revision OR e.proof->'members' IS DISTINCT FROM head.members OR e.proof->'retired' IS DISTINCT FROM nerocd_database_connection_transition_retired(db_plan,head.members,head.retired) OR db_healthy IS DISTINCT FROM (SELECT healthy FROM hosting_binding_state WHERE binding_id=b.database_binding_id) OR NOT EXISTS(SELECT 1 FROM hosting_database_network_holds h WHERE h.database_binding_id=b.database_binding_id AND h.connection_id=c.id AND h.operation_id=o.id)) THEN RAISE EXCEPTION 'connection current membership or hold mismatch' USING ERRCODE='23514'; END IF;
 SELECT body INTO plan FROM hosting_apply_plans WHERE operation_id=o.id;
 IF FOUND THEN
  IF plan->'snapshot' IS DISTINCT FROM snap OR plan->>'hash' IS DISTINCT FROM (SELECT body_hash FROM hosting_apply_plans WHERE operation_id=o.id) THEN RAISE EXCEPTION 'connection plan snapshot mismatch' USING ERRCODE='23514'; END IF;
  IF e.family='application' THEN
   IF plan->>'compiler_version' IS DISTINCT FROM (CASE WHEN snap ? 'storage' THEN 'image-hosting.application-database.owned-storage.v1' ELSE 'image-hosting.runtime-env.database.v1' END) OR plan->'application_database'->'binding' IS DISTINCT FROM b.body OR plan->'application_database'->>'operation_id'<>o.id OR plan->'application_database'->>'create_nonce' IS DISTINCT FROM e.proof->>'create_nonce' OR plan->'application_database'->>'database_plan_hash' IS DISTINCT FROM db_plan->>'hash' OR plan->'application_database'->'origin' IS DISTINCT FROM origin->'origin' OR plan->'application_database'->>'membership_revision' IS DISTINCT FROM e.expected_membership_revision::text OR plan ? 'database' OR (plan ? 'storage') IS DISTINCT FROM (snap ? 'storage') THEN RAISE EXCEPTION 'application connection plan substitution' USING ERRCODE='23514'; END IF;
   IF snap ? 'storage' AND (plan->'storage'->'reference' IS DISTINCT FROM snap->'storage' OR coalesce(plan->>'retain_previous','false') IS DISTINCT FROM 'false' OR NOT EXISTS(SELECT 1 FROM hosting_storage st WHERE st.id=snap->'storage'->>'storage_id' AND st.volume_name=plan->'storage'->>'volume_name' AND st.mount_path=plan->'storage'->>'mount_path')) THEN RAISE EXCEPTION 'connected storage descriptor mismatch' USING ERRCODE='23514'; END IF;
  ELSE
   IF plan->>'compiler_version'<>'database-hosting.private.membership.v2' OR plan->'release' IS DISTINCT FROM db_plan->'release' OR plan->'storage' IS DISTINCT FROM db_plan->'storage' OR (plan->'database')-'network' IS DISTINCT FROM (db_plan->'database')-'network' OR plan->'previous_healthy' IS DISTINCT FROM db_healthy OR plan->'database'->'network'->>'membership_revision' IS DISTINCT FROM e.expected_membership_revision::text OR plan->'database'->'network'->>'network_id' IS DISTINCT FROM origin->'origin'->>'network_id' OR plan ? 'application_database' THEN RAISE EXCEPTION 'member maintenance plan substitution' USING ERRCODE='23514'; END IF;
  END IF;
 END IF;
END $$;


CREATE FUNCTION nerocd_appdb_storage_schema_compatible() RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT NOT EXISTS(SELECT 1 FROM (VALUES ('hosting_application_database_storage','hosting_appdb_storage_immutable'),('hosting_application_database_storage_writers','hosting_appdb_storage_writers_immutable'),('hosting_application_database_storage_recoveries','hosting_appdb_storage_recoveries_immutable')) required(table_name,trigger_name) WHERE NOT EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass(current_schema()||'.'||table_name) AND tgname=trigger_name AND tgenabled IN('O','A')));
$$;
DO $$ DECLARE owner_schema text:=current_schema(); BEGIN
 EXECUTE format('ALTER FUNCTION %I.nerocd_database_connection_validate(text,boolean) SET search_path=%I,pg_catalog,pg_temp',owner_schema,owner_schema);
 EXECUTE format('ALTER FUNCTION %I.nerocd_appdb_storage_schema_compatible() SET search_path=%I,pg_catalog,pg_temp',owner_schema,owner_schema);
END $$;

CREATE TABLE hosting_application_database_storage_effects (
 authorization_id text PRIMARY KEY,
 operation_id text NOT NULL REFERENCES hosting_application_database_storage(operation_id) ON DELETE RESTRICT,
 request jsonb NOT NULL CHECK(jsonb_typeof(request)='object' AND pg_column_size(request)<=16384),
 response jsonb NOT NULL CHECK(jsonb_typeof(response)='object' AND pg_column_size(response)<=16384),
 use_until timestamptz NOT NULL,
 CHECK((request->>'operation_id'=operation_id AND request->>'authorization_id'=authorization_id AND request->>'action' IN('stop_predecessor','stop_candidate','resume_predecessor') AND response->>'action'=request->>'action' AND response->>'state_hash'=request->>'state_hash') IS TRUE)
);
CREATE TRIGGER hosting_appdb_storage_effects_immutable BEFORE UPDATE OR DELETE ON hosting_application_database_storage_effects FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_execution_immutable();
ALTER TABLE hosting_task_targets DROP CONSTRAINT hosting_task_targets_mode_check;
ALTER TABLE hosting_task_targets ADD CONSTRAINT hosting_task_targets_mode_check CHECK(mode IN ('deploy','observe','cleanup','finalize','storage_recover'));
CREATE UNIQUE INDEX hosting_one_storage_recovery_task ON hosting_task_targets(operation_id) WHERE mode='storage_recover';

CREATE TABLE hosting_application_database_storage_membership_receipts(
 database_binding_id text NOT NULL REFERENCES resource_bindings(id),
 revision bigint NOT NULL CHECK(revision>1),
 operation_id text NOT NULL UNIQUE REFERENCES hosting_application_database_storage(operation_id),
 body jsonb NOT NULL CHECK(jsonb_typeof(body)='object' AND pg_column_size(body)<=65536),
 body_hash text NOT NULL CHECK(body_hash ~ '^sha256:[0-9a-f]{64}$'),
 PRIMARY KEY(database_binding_id,revision)
);
CREATE TRIGGER hosting_appdb_storage_membership_immutable BEFORE UPDATE OR DELETE ON hosting_application_database_storage_membership_receipts FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_execution_immutable();

CREATE OR REPLACE FUNCTION nerocd_database_membership_advance() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE attached hosting_database_membership_receipts; retired_receipt hosting_database_membership_receipts; stage hosting_application_database_stages; e hosting_database_connection_executions; healthy jsonb;
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'membership head cannot be deleted' USING ERRCODE='23514'; END IF;
 IF TG_OP='INSERT' THEN
  IF NEW.revision<>1 OR NEW.members<>'[]'::jsonb OR NEW.retired<>'[]'::jsonb OR NOT EXISTS(SELECT 1 FROM hosting_database_connections c JOIN hosting_database_bindings b ON b.id=c.binding_id JOIN hosting_operations o ON o.id=c.upgrade_operation_id JOIN hosting_apply_plans p ON p.operation_id=o.id JOIN hosting_database_network_origin_receipts receipt ON receipt.capture_id=c.capture_id WHERE c.id=NEW.connection_id AND b.database_binding_id=NEW.database_binding_id AND o.state='succeeded' AND receipt.receipt_id=NEW.origin_receipt_id AND p.body->'database'->'network'->'members'='[]'::jsonb AND p.body->'database'->'network'->>'membership_revision'='1') THEN RAISE EXCEPTION 'membership head requires settled empty upgrade' USING ERRCODE='23514'; END IF;
  RETURN NEW;
 END IF;
 IF (NEW.database_binding_id,NEW.connection_id,NEW.origin_receipt_id) IS DISTINCT FROM (OLD.database_binding_id,OLD.connection_id,OLD.origin_receipt_id) OR NEW.revision NOT IN(OLD.revision+1,OLD.revision+2) THEN RAISE EXCEPTION 'membership revision or identity mismatch' USING ERRCODE='23514'; END IF;

 IF NEW.revision=OLD.revision+1 AND NEW.retired=OLD.retired AND jsonb_array_length(OLD.members)=1 AND jsonb_array_length(NEW.members)=1 AND (NEW.members->0)-'resume'=(OLD.members->0)-'resume' AND NEW.members->0 ? 'resume' THEN
  IF NOT EXISTS(SELECT 1 FROM hosting_application_database_storage_membership_receipts r JOIN hosting_database_connection_executions execution ON execution.operation_id=r.operation_id JOIN hosting_operations operation ON operation.id=r.operation_id JOIN hosting_application_database_storage_writers writer ON writer.operation_id=r.operation_id AND writer.phase='resumed' JOIN hosting_application_database_storage_recoveries recovery ON recovery.operation_id=r.operation_id AND recovery.action='resume_predecessor' JOIN run_leases lease ON lease.id=writer.request->>'lease_id' JOIN hosting_binding_state appstate ON appstate.binding_id=operation.binding_id WHERE r.database_binding_id=OLD.database_binding_id AND r.revision=NEW.revision AND r.body=NEW.members->0 AND execution.expected_membership_revision=OLD.revision AND execution.connection_id=OLD.connection_id AND execution.proof->'members'=OLD.members AND operation.state='failed' AND recovery.run_id=lease.run_id AND lease.status='active' AND lease.expires_at>clock_timestamp() AND writer.request->>'run_id'=lease.run_id AND writer.request->>'fence'=lease.fence AND writer.request->>'attempt'=lease.attempt::text AND r.body->'resume'->>'version'='application-database.storage.v1' AND r.body->'resume'->>'operation_id'=operation.id AND r.body->'resume'->>'membership_revision'=NEW.revision::text AND r.body->'resume'->'writer_receipt'=writer.request->'receipt' AND r.body->'resume'->>'original_receipt_hash'=OLD.members->0->'receipt'->>'hash' AND writer.request->'receipt'->'predecessor_endpoint'->>'present'='true' AND writer.request->'receipt'->'predecessor_endpoint'->'endpoint'->'target'=OLD.members->0->'receipt'->'observation'->'endpoint'->'target' AND writer.request->'receipt'->'predecessor_endpoint'->'endpoint'->>'endpoint_id' ~ '^[0-9a-f]{64}$' AND appstate.healthy=OLD.members->0->'application_healthy' AND appstate.hold_operation_id=operation.id AND EXISTS(SELECT 1 FROM hosting_database_network_holds hold WHERE hold.database_binding_id=OLD.database_binding_id AND hold.operation_id=operation.id)) THEN RAISE EXCEPTION 'resumed membership requires exact fresh recovery proof' USING ERRCODE='23514'; END IF;
  RETURN NEW;
 END IF;
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

CREATE OR REPLACE FUNCTION nerocd_database_connection_hold_release() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE e hosting_database_connection_executions;
BEGIN
 SELECT * INTO STRICT e FROM hosting_database_connection_executions WHERE operation_id=OLD.operation_id AND connection_id=OLD.connection_id;

 IF EXISTS(SELECT 1 FROM hosting_application_database_storage_membership_receipts receipt JOIN hosting_database_membership_heads head ON head.database_binding_id=receipt.database_binding_id AND head.revision=receipt.revision JOIN hosting_application_database_storage_writers writer ON writer.operation_id=receipt.operation_id AND writer.phase='resumed' JOIN hosting_operations operation ON operation.id=receipt.operation_id WHERE receipt.operation_id=e.operation_id AND receipt.database_binding_id=OLD.database_binding_id AND head.connection_id=OLD.connection_id AND head.members=jsonb_build_array(receipt.body) AND receipt.revision=e.expected_membership_revision+1 AND operation.state='failed' AND receipt.body->'resume'->'writer_receipt'=writer.request->'receipt') THEN RETURN OLD; END IF;
 IF NOT EXISTS(SELECT 1 FROM hosting_operations o JOIN hosting_receipts r ON r.operation_id=o.id AND r.phase='complete' WHERE o.id=e.operation_id AND o.state='succeeded' AND r.body->>'outcome'='succeeded' AND r.body->>'ready'='true' AND coalesce(r.body->>'uncertain','false')='false') THEN RAISE EXCEPTION 'unresolved database network hold cannot be released' USING ERRCODE='23514'; END IF;
 IF e.family='application' AND NOT EXISTS(SELECT 1 FROM hosting_database_membership_heads head JOIN hosting_database_membership_receipts receipt ON receipt.database_binding_id=head.database_binding_id AND receipt.revision=head.revision WHERE head.database_binding_id=OLD.database_binding_id AND head.connection_id=OLD.connection_id AND receipt.operation_id=e.operation_id) THEN RAISE EXCEPTION 'network hold release requires atomic membership settlement' USING ERRCODE='23514'; END IF;
 RETURN OLD;
END $$;

CREATE FUNCTION nerocd_appdb_storage_writer_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE op hosting_operations; lease run_leases; target hosting_task_targets; prior hosting_application_database_storage_writers; stage hosting_application_database_stages; plan jsonb; expected_phase text; recovering boolean;
BEGIN
 SELECT * INTO STRICT op FROM hosting_operations WHERE id=NEW.operation_id FOR UPDATE;
 SELECT body INTO STRICT plan FROM hosting_apply_plans WHERE operation_id=op.id;
 SELECT * INTO STRICT lease FROM run_leases WHERE id=NEW.request->>'lease_id' FOR SHARE;
 SELECT * INTO STRICT target FROM hosting_task_targets WHERE run_id=lease.run_id;
 recovering:=NEW.phase IN('resume_intent','resumed');
 IF lease.status<>'active' OR lease.expires_at<=clock_timestamp() OR lease.run_id IS DISTINCT FROM NEW.request->>'run_id' OR lease.attempt::text IS DISTINCT FROM NEW.request->>'attempt' OR lease.fence IS DISTINCT FROM NEW.request->>'fence' OR target.operation_id<>op.id OR target.runner_id<>lease.runner_id OR lease.runner_id IS DISTINCT FROM plan->'snapshot'->>'runner_id' OR NEW.request->'receipt'->>'plan_hash' IS DISTINCT FROM plan->>'hash' OR NEW.request->'receipt'->'storage_ref' IS DISTINCT FROM plan->'snapshot'->'storage' OR NOT EXISTS(SELECT 1 FROM hosting_binding_state WHERE binding_id=op.binding_id AND hold_operation_id=op.id) OR EXISTS(SELECT 1 FROM hosting_application_database_yields WHERE operation_id=op.id) THEN RAISE EXCEPTION 'storage writer requires exact current authority and held plan' USING ERRCODE='23514'; END IF;
 IF recovering THEN
  IF target.mode<>'storage_recover' OR op.state<>'failed' OR NOT EXISTS(SELECT 1 FROM hosting_application_database_storage_recoveries r WHERE r.operation_id=op.id AND r.action='resume_predecessor' AND r.run_id=lease.run_id) THEN RAISE EXCEPTION 'storage resume requires fresh dedicated recovery task' USING ERRCODE='23514'; END IF;
 ELSIF target.mode<>'deploy' OR lease.run_id<>op.run_id OR op.state NOT IN('applying','verifying') THEN RAISE EXCEPTION 'storage stop requires original active deployment' USING ERRCODE='23514'; END IF;
 expected_phase:=CASE NEW.phase WHEN 'stopped' THEN 'stop_intent' WHEN 'candidate_stopped' THEN 'stopped' WHEN 'replacement_failed' THEN 'candidate_stopped' WHEN 'resume_intent' THEN 'replacement_failed' WHEN 'resumed' THEN 'resume_intent' ELSE NULL END;
 IF expected_phase IS NULL THEN
  IF EXISTS(SELECT 1 FROM hosting_application_database_storage_writers WHERE operation_id=op.id) OR NEW.request->'receipt'->>'previous_state_hash' IS DISTINCT FROM '' THEN RAISE EXCEPTION 'storage writer initial intent is not empty' USING ERRCODE='23514'; END IF;
 ELSE
  SELECT * INTO STRICT prior FROM hosting_application_database_storage_writers WHERE operation_id=op.id AND phase=expected_phase;
  IF NEW.request->'receipt'->>'previous_state_hash' IS DISTINCT FROM prior.request->'receipt'->>'hash' THEN RAISE EXCEPTION 'storage writer chain mismatch' USING ERRCODE='23514'; END IF;
 END IF;
 SELECT * INTO stage FROM hosting_application_database_stages WHERE operation_id=op.id ORDER BY CASE phase WHEN 'uncertain' THEN 6 WHEN 'ready' THEN 5 WHEN 'attached' THEN 4 WHEN 'network_configured' THEN 3 WHEN 'attach_intent' THEN 2 ELSE 1 END DESC LIMIT 1;
 IF (stage.operation_id IS NULL AND (NEW.request->'receipt'->>'stage_hash' IS DISTINCT FROM '' OR NEW.request->'receipt'->>'candidate_hash' IS DISTINCT FROM '')) OR (stage.operation_id IS NOT NULL AND (NEW.request->'receipt'->>'stage_hash' IS DISTINCT FROM stage.stage_hash OR NEW.request->'receipt'->>'candidate_hash' IS DISTINCT FROM stage.stage->'candidate'->>'hash')) THEN RAISE EXCEPTION 'storage writer stage mismatch' USING ERRCODE='23514'; END IF;
 IF NEW.phase IN('stopped','candidate_stopped','resumed') AND NOT EXISTS(SELECT 1 FROM hosting_application_database_storage_effects a WHERE a.operation_id=op.id AND a.request->>'run_id'=lease.run_id AND a.request->>'lease_id'=lease.id AND a.request->>'fence'=lease.fence AND a.request->>'attempt'=lease.attempt::text AND a.request->>'state_hash'=prior.request->'receipt'->>'hash' AND a.request->>'action'=CASE NEW.phase WHEN 'stopped' THEN 'stop_predecessor' WHEN 'candidate_stopped' THEN 'stop_candidate' ELSE 'resume_predecessor' END) THEN RAISE EXCEPTION 'storage observation lacks admitted effect' USING ERRCODE='23514'; END IF;
 IF (NEW.phase='resumed') IS DISTINCT FROM (NEW.request->'receipt' ? 'predecessor_endpoint') THEN RAISE EXCEPTION 'storage resume endpoint evidence mismatch' USING ERRCODE='23514'; END IF;
 PERFORM nerocd_database_connection_validate(op.id,true);
 RETURN NEW;
END $$;
CREATE TRIGGER hosting_appdb_storage_writer_authority BEFORE INSERT ON hosting_application_database_storage_writers FOR EACH ROW EXECUTE FUNCTION nerocd_appdb_storage_writer_guard();

CREATE FUNCTION nerocd_appdb_storage_effect_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE op hosting_operations; lease run_leases; writer hosting_application_database_storage_writers; stage hosting_application_database_stages; action_key text; expected_phase text;
BEGIN
 SELECT * INTO STRICT op FROM hosting_operations WHERE id=NEW.operation_id FOR UPDATE;
 SELECT * INTO STRICT lease FROM run_leases WHERE id=NEW.request->>'lease_id' FOR SHARE;
 action_key:=NEW.request->>'action';expected_phase:=CASE action_key WHEN 'stop_predecessor' THEN 'stop_intent' WHEN 'stop_candidate' THEN 'stopped' WHEN 'resume_predecessor' THEN 'resume_intent' END;
 SELECT * INTO STRICT writer FROM hosting_application_database_storage_writers WHERE operation_id=op.id AND phase=expected_phase;
 IF lease.status<>'active' OR lease.expires_at<=clock_timestamp() OR lease.run_id IS DISTINCT FROM NEW.request->>'run_id' OR lease.attempt::text IS DISTINCT FROM NEW.request->>'attempt' OR lease.fence IS DISTINCT FROM NEW.request->>'fence' OR NEW.request->>'state_hash' IS DISTINCT FROM writer.request->'receipt'->>'hash' OR NEW.use_until>least(lease.expires_at,clock_timestamp()+interval '30 seconds') OR NEW.use_until<=clock_timestamp() OR NEW.response->>'use_until' IS NULL OR (NEW.response->>'use_until')::timestamptz<>NEW.use_until OR EXISTS(SELECT 1 FROM hosting_application_database_yields WHERE operation_id=op.id) OR NOT EXISTS(SELECT 1 FROM hosting_binding_state WHERE binding_id=op.binding_id AND hold_operation_id=op.id) THEN RAISE EXCEPTION 'storage effect current authority mismatch' USING ERRCODE='23514'; END IF;
 IF action_key='resume_predecessor' THEN
  IF op.state<>'failed' OR NOT EXISTS(SELECT 1 FROM hosting_application_database_storage_recoveries recovery JOIN hosting_task_targets t ON t.run_id=recovery.run_id AND t.mode='storage_recover' WHERE recovery.operation_id=op.id AND recovery.action='resume_predecessor' AND recovery.run_id=lease.run_id) THEN RAISE EXCEPTION 'storage resume lacks fresh recovery' USING ERRCODE='23514'; END IF;
 ELSIF lease.run_id<>op.run_id OR op.state NOT IN('applying','verifying') THEN RAISE EXCEPTION 'storage stop lacks original deployment authority' USING ERRCODE='23514'; END IF;
 SELECT * INTO stage FROM hosting_application_database_stages WHERE operation_id=op.id ORDER BY CASE phase WHEN 'uncertain' THEN 6 WHEN 'ready' THEN 5 WHEN 'attached' THEN 4 WHEN 'network_configured' THEN 3 WHEN 'attach_intent' THEN 2 ELSE 1 END DESC LIMIT 1;
 IF coalesce(stage.stage_hash,'') IS DISTINCT FROM NEW.request->>'stage_hash' OR (action_key='stop_predecessor' AND stage.operation_id IS NOT NULL) OR (action_key<>'stop_predecessor' AND stage.operation_id IS NULL) THEN RAISE EXCEPTION 'storage effect stage mismatch' USING ERRCODE='23514'; END IF;
 PERFORM nerocd_database_connection_validate(op.id,true);
 RETURN NEW;
END $$;
CREATE TRIGGER hosting_appdb_storage_effect_authority BEFORE INSERT ON hosting_application_database_storage_effects FOR EACH ROW EXECUTE FUNCTION nerocd_appdb_storage_effect_guard();

CREATE FUNCTION nerocd_appdb_storage_create_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF EXISTS(SELECT 1 FROM hosting_application_database_storage WHERE operation_id=NEW.operation_id) AND NOT EXISTS(SELECT 1 FROM hosting_application_database_storage_writers WHERE operation_id=NEW.operation_id AND phase='stopped') THEN RAISE EXCEPTION 'connected storage candidate requires durable stopped predecessor' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER hosting_appdb_storage_candidate_effect BEFORE INSERT ON hosting_application_database_effect_authorizations FOR EACH ROW EXECUTE FUNCTION nerocd_appdb_storage_create_guard();
CREATE TRIGGER hosting_appdb_storage_candidate_stage BEFORE INSERT ON hosting_application_database_stages FOR EACH ROW EXECUTE FUNCTION nerocd_appdb_storage_create_guard();

CREATE FUNCTION nerocd_appdb_storage_recovery_task_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE target hosting_task_targets; recovery hosting_application_database_storage_recoveries; op hosting_operations; task task_runs;
BEGIN
 IF TG_TABLE_NAME='hosting_task_targets' THEN target:=NEW;
 ELSE SELECT * INTO target FROM hosting_task_targets WHERE run_id=NEW.run_id; END IF;
 IF target.mode IS DISTINCT FROM 'storage_recover' THEN RETURN NEW; END IF;
 SELECT * INTO STRICT recovery FROM hosting_application_database_storage_recoveries WHERE operation_id=target.operation_id AND action='resume_predecessor' AND run_id=target.run_id;
 SELECT * INTO STRICT op FROM hosting_operations WHERE id=target.operation_id AND state='failed';
 SELECT * INTO STRICT task FROM task_runs WHERE id=target.run_id;
 IF target.run_id<>'storage_recover_'||op.id OR target.binding_id<>op.binding_id OR target.runner_id IS DISTINCT FROM (SELECT body->>'runner_id' FROM hosting_snapshots WHERE operation_id=op.id) OR task.requested_by<>recovery.actor_id OR task.project_id<>op.project_id OR task.run_spec IS DISTINCT FROM jsonb_build_object('type','image_deploy','hosting_operation_id',op.id,'hosting_mode','storage_recover','inputs','{}'::jsonb) OR task.workflow<>'{}'::jsonb OR EXISTS(SELECT 1 FROM hosting_application_database_yields WHERE operation_id=op.id) OR NOT EXISTS(SELECT 1 FROM hosting_application_database_storage_writers WHERE operation_id=op.id AND phase='replacement_failed') OR EXISTS(SELECT 1 FROM run_leases WHERE run_id=op.run_id AND status='active' AND expires_at>clock_timestamp()) THEN RAISE EXCEPTION 'storage recovery task lacks exact failed writer association' USING ERRCODE='23514'; END IF;
 IF TG_TABLE_NAME='run_leases' THEN
 IF NEW.attempt<>1 OR NEW.runner_id<>target.runner_id OR EXISTS(SELECT 1 FROM run_leases WHERE run_id=target.run_id) THEN RAISE EXCEPTION 'storage recovery cannot retry expired effects' USING ERRCODE='23514'; END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER hosting_appdb_storage_recovery_target BEFORE INSERT ON hosting_task_targets FOR EACH ROW EXECUTE FUNCTION nerocd_appdb_storage_recovery_task_guard();
CREATE TRIGGER hosting_appdb_storage_recovery_lease BEFORE INSERT ON run_leases FOR EACH ROW EXECUTE FUNCTION nerocd_appdb_storage_recovery_task_guard();

CREATE OR REPLACE FUNCTION nerocd_appdb_proxy_association() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE c hosting_application_database_route_continuations; o hosting_operations; prior hosting_proxy_operations; snap jsonb; expected_routes jsonb; expected_selected jsonb;
BEGIN
 SELECT * INTO c FROM hosting_application_database_route_continuations WHERE proxy_operation_id=NEW.id OR operation_id IN(SELECT operation_id FROM hosting_application_database_storage_recoveries WHERE proxy_operation_id=NEW.id AND action='complete_replacement');
 IF NOT FOUND THEN
  IF EXISTS(SELECT 1 FROM jsonb_array_elements(NEW.snapshot->'routes') r JOIN hosting_application_database_route_continuations continuation ON continuation.operation_id=r->>'release_operation_id' JOIN hosting_operations candidate ON candidate.id=continuation.operation_id WHERE candidate.state<>'succeeded') THEN RAISE EXCEPTION 'held connected candidate requires exact reserved proxy operation' USING ERRCODE='23514'; END IF;
  RETURN NULL;
 END IF;
 SELECT * INTO STRICT o FROM hosting_operations WHERE id=c.operation_id AND state IN('applying','verifying');
 SELECT * INTO STRICT prior FROM hosting_proxy_operations WHERE id=c.body->'request'->>'expected_loaded_proxy_operation_id' AND proxy_id=c.proxy_id AND state='applied';
 SELECT body INTO STRICT snap FROM hosting_snapshots WHERE operation_id=o.id;
 IF NOT EXISTS(SELECT 1 FROM hosting_application_database_yields WHERE operation_id=o.id) OR NEW.proxy_id<>c.proxy_id OR NEW.created_by<>o.created_by OR NEW.project_id IS DISTINCT FROM o.project_id OR NEW.snapshot - 'generation' - 'routes' - 'verified_switch' - 'storage_switch' IS DISTINCT FROM prior.snapshot - 'generation' - 'routes' - 'verified_switch' - 'storage_switch' THEN RAISE EXCEPTION 'connected proxy association mismatch' USING ERRCODE='23514'; END IF;
 SELECT jsonb_agg(CASE WHEN r->>'resource_id'=o.resource_id THEN jsonb_set(jsonb_set(r,'{release_operation_id}',to_jsonb(o.id)),'{host_port}',snap->'host_port') ELSE r END ORDER BY r->>'route_id') INTO expected_routes FROM jsonb_array_elements(prior.snapshot->'routes') r;
 SELECT jsonb_agg(jsonb_build_object('route_id',pick->>'route_id','prior_version',to_jsonb((pick->>'expected_version')::bigint+CASE WHEN NEW.id=c.proxy_operation_id THEN 0 ELSE 2 END),'prior_target',r) ORDER BY pick->>'route_id') INTO expected_selected FROM jsonb_array_elements(c.body->'request'->'routes') pick JOIN jsonb_array_elements(prior.snapshot->'routes') r ON r->>'route_id'=pick->>'route_id';
 IF NEW.snapshot->'routes' IS DISTINCT FROM expected_routes OR NEW.snapshot->'verified_switch' IS DISTINCT FROM jsonb_build_object('version',1,'expected_loaded_generation',c.body->'request'->'expected_loaded_generation','prior_applied_proxy_operation_id',prior.id,'routes',expected_selected) THEN RAISE EXCEPTION 'connected switch changed frozen route or predecessor scope' USING ERRCODE='23514'; END IF;
 IF EXISTS(SELECT 1 FROM hosting_application_database_storage WHERE operation_id=o.id) THEN
 IF NEW.snapshot->'storage_switch'->>'operation_id' IS DISTINCT FROM o.id OR NEW.snapshot->'storage_switch'->>'continuation_hash' IS DISTINCT FROM c.body_hash OR NEW.snapshot->'storage_switch'->'storage_ref' IS DISTINCT FROM snap->'storage' OR NEW.snapshot->'storage_switch'->>'candidate_hash' IS DISTINCT FROM (SELECT request->>'candidate_hash' FROM hosting_application_database_yields WHERE operation_id=o.id) OR NOT EXISTS(SELECT 1 FROM hosting_application_database_storage_writers writer WHERE writer.operation_id=o.id AND writer.phase='stopped' AND writer.request->'receipt'->>'hash'=NEW.snapshot->'storage_switch'->>'writer_state_hash') THEN RAISE EXCEPTION 'interrupted proxy lacks exact stopped writer proof' USING ERRCODE='23514'; END IF;
 ELSIF NEW.snapshot ? 'storage_switch' THEN RAISE EXCEPTION 'storage switch proof requires composed operation' USING ERRCODE='23514'; END IF;
 RETURN NULL;
END $$;

CREATE OR REPLACE FUNCTION nerocd_appdb_route_applied(operation_key text) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT EXISTS(SELECT 1 FROM hosting_application_database_route_continuations c
 JOIN hosting_application_database_yields y ON y.operation_id=c.operation_id
 JOIN hosting_proxy_operations p ON p.id=coalesce((SELECT proxy_operation_id FROM hosting_application_database_storage_recoveries WHERE operation_id=c.operation_id AND action='complete_replacement'),c.proxy_operation_id) AND p.proxy_id=c.proxy_id
 JOIN hosting_proxy_receipts receipt ON receipt.operation_id=p.id AND receipt.phase='complete'
 JOIN hosting_proxies proxy ON proxy.id=p.proxy_id
 WHERE c.operation_id=operation_key AND p.state='applied' AND NOT EXISTS(SELECT 1 FROM audit_events WHERE target_id=p.id AND action='hosting.route_switch_metadata_conflict')
 AND receipt.body->>'outcome'='applied' AND receipt.body->>'plan_hash'=p.plan_hash
 AND receipt.body->>'loaded_generation'=p.generation::text AND proxy.loaded_generation=p.generation
 AND NOT EXISTS(SELECT 1 FROM hosting_proxy_restore_holds WHERE proxy_id=proxy.id) AND proxy.hold_operation_id IS NULL AND proxy.active_operation_id IS NULL
 AND p.snapshot->'verified_switch'->>'prior_applied_proxy_operation_id'=c.body->'request'->>'expected_loaded_proxy_operation_id'
 AND p.snapshot->'verified_switch'->>'expected_loaded_generation'=c.body->'request'->>'expected_loaded_generation');
$$;


CREATE OR REPLACE FUNCTION nerocd_database_connection_retirement_validate() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE e hosting_database_connection_executions; ready hosting_application_database_stages; intent hosting_application_database_retirements; prior jsonb; lease run_leases;
BEGIN
 SELECT * INTO STRICT e FROM hosting_database_connection_executions WHERE operation_id=NEW.operation_id AND family='application';
 SELECT * INTO STRICT ready FROM hosting_application_database_stages WHERE operation_id=NEW.operation_id AND phase='ready';
 IF EXISTS(SELECT 1 FROM hosting_application_database_stages WHERE operation_id=NEW.operation_id AND phase='uncertain') OR jsonb_array_length(e.proof->'members')<>1 THEN RAISE EXCEPTION 'retirement requires ready replacement and one predecessor' USING ERRCODE='23514'; END IF;
 prior:=e.proof->'members'->0;
 SELECT * INTO STRICT lease FROM run_leases WHERE id=NEW.authority->>'lease_id';
 IF lease.run_id IS DISTINCT FROM NEW.authority->>'run_id' OR lease.attempt::text IS DISTINCT FROM NEW.authority->>'attempt' OR lease.fence IS DISTINCT FROM NEW.authority->>'fence' OR NEW.plan->'target' IS DISTINCT FROM prior->'endpoint_plan'->'target' OR NEW.plan->>'expected_endpoint_id' IS DISTINCT FROM coalesce(prior->'resume'->'writer_receipt'->'predecessor_endpoint'->'endpoint',prior->'receipt'->'observation'->'endpoint')->>'endpoint_id' OR NEW.plan->>'membership_revision' IS DISTINCT FROM (e.expected_membership_revision+2)::text OR NEW.plan->>'binding_hash' IS DISTINCT FROM e.proof->'binding'->>'hash' OR NEW.receipt->>'binding_hash' IS DISTINCT FROM e.proof->'binding'->>'hash' OR NEW.receipt->>'membership_revision' IS DISTINCT FROM NEW.plan->>'membership_revision' THEN RAISE EXCEPTION 'retirement predecessor or authority mismatch' USING ERRCODE='23514'; END IF;
 IF NEW.phase='detached' THEN
  SELECT * INTO STRICT intent FROM hosting_application_database_retirements WHERE operation_id=NEW.operation_id AND phase='retire_intent';
  IF NEW.plan IS DISTINCT FROM intent.plan OR NEW.authority IS DISTINCT FROM intent.authority OR NEW.receipt->'observation'->'endpoint' IS DISTINCT FROM intent.receipt->'intent' OR NEW.receipt->'observation'->'present' IS DISTINCT FROM 'false'::jsonb THEN RAISE EXCEPTION 'retirement detachment does not match exact intent' USING ERRCODE='23514'; END IF;
 ELSE
  IF NEW.receipt->'intent' IS DISTINCT FROM coalesce(prior->'resume'->'writer_receipt'->'predecessor_endpoint'->'endpoint',prior->'receipt'->'observation'->'endpoint') THEN RAISE EXCEPTION 'retirement intent changed attached endpoint' USING ERRCODE='23514'; END IF;
 END IF;
 RETURN NEW;
END $$;

CREATE OR REPLACE FUNCTION nerocd_appdb_storage_schema_compatible() RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT NOT EXISTS(SELECT 1 FROM (VALUES
 ('hosting_application_database_storage','hosting_appdb_storage_immutable'),
 ('hosting_application_database_storage_writers','hosting_appdb_storage_writers_immutable'),
 ('hosting_application_database_storage_writers','hosting_appdb_storage_writer_authority'),
 ('hosting_application_database_storage_writers','hosting_appdb_storage_writer_settlement'),
 ('hosting_application_database_storage_recoveries','hosting_appdb_storage_recoveries_immutable'),
 ('hosting_application_database_storage_recoveries','hosting_appdb_storage_recovery_admission'),
 ('hosting_application_database_storage_effects','hosting_appdb_storage_effects_immutable'),
 ('hosting_application_database_storage_effects','hosting_appdb_storage_effect_authority'),
 ('hosting_application_database_storage_membership_receipts','hosting_appdb_storage_membership_immutable'),
 ('hosting_application_database_effect_authorizations','hosting_appdb_storage_candidate_effect'),
 ('hosting_application_database_stages','hosting_appdb_storage_candidate_stage'),
 ('hosting_task_targets','hosting_appdb_storage_recovery_target'),('run_leases','hosting_appdb_storage_recovery_lease')
 ) required(table_name,trigger_name) WHERE NOT EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass(current_schema()||'.'||table_name) AND tgname=trigger_name AND tgenabled IN('O','A')));
$$;
DO $$ DECLARE owner_schema text:=current_schema(); routine text; BEGIN
 FOREACH routine IN ARRAY ARRAY['nerocd_database_connection_validate(text,boolean)','nerocd_appdb_storage_schema_compatible()','nerocd_database_membership_advance()','nerocd_database_connection_hold_release()','nerocd_appdb_storage_writer_guard()','nerocd_appdb_storage_effect_guard()','nerocd_appdb_storage_create_guard()','nerocd_appdb_storage_recovery_task_guard()','nerocd_appdb_proxy_association()','nerocd_appdb_route_applied(text)','nerocd_database_connection_retirement_validate()'] LOOP EXECUTE format('ALTER FUNCTION %I.%s SET search_path=%I,pg_catalog,pg_temp',owner_schema,routine,owner_schema); END LOOP;
END $$;

CREATE FUNCTION nerocd_appdb_storage_writer_settlement() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE expected_status text; op hosting_operations;
BEGIN
 IF NEW.phase NOT IN('replacement_failed','resumed') THEN RETURN NULL; END IF;
 expected_status:=CASE NEW.phase WHEN 'resumed' THEN 'succeeded' ELSE 'failed' END;
 SELECT * INTO STRICT op FROM hosting_operations WHERE id=NEW.operation_id;
 IF op.state<>'failed' OR NOT EXISTS(SELECT 1 FROM run_leases lease JOIN task_runs task ON task.id=lease.run_id WHERE lease.id=NEW.request->>'lease_id' AND lease.run_id=NEW.request->>'run_id' AND lease.fence=NEW.request->>'fence' AND lease.attempt::text=NEW.request->>'attempt' AND lease.status=expected_status AND task.status=expected_status AND lease.completion_key=NEW.receipt_id) THEN RAISE EXCEPTION 'storage terminal writer requires exact independent task settlement' USING ERRCODE='23514'; END IF;
 IF NEW.phase='replacement_failed' THEN
 IF NOT EXISTS(SELECT 1 FROM hosting_binding_state WHERE binding_id=op.binding_id AND hold_operation_id=op.id) OR NOT EXISTS(SELECT 1 FROM hosting_database_network_holds WHERE operation_id=op.id) THEN RAISE EXCEPTION 'failed replacement must retain holds' USING ERRCODE='23514'; END IF;
 ELSE
 IF EXISTS(SELECT 1 FROM hosting_binding_state WHERE hold_operation_id=op.id) OR EXISTS(SELECT 1 FROM hosting_database_network_holds WHERE operation_id=op.id) OR NOT EXISTS(SELECT 1 FROM hosting_application_database_storage_membership_receipts receipt JOIN hosting_database_membership_heads head ON head.database_binding_id=receipt.database_binding_id AND head.revision=receipt.revision WHERE receipt.operation_id=op.id AND head.members=jsonb_build_array(receipt.body) AND receipt.body->'resume'->'writer_receipt'=NEW.request->'receipt') THEN RAISE EXCEPTION 'resumed recovery requires atomic membership and hold settlement' USING ERRCODE='23514'; END IF;
 END IF;
 RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER hosting_appdb_storage_writer_settlement AFTER INSERT ON hosting_application_database_storage_writers DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION nerocd_appdb_storage_writer_settlement();
DO $$ DECLARE owner_schema text:=current_schema(); BEGIN EXECUTE format('ALTER FUNCTION %I.nerocd_appdb_storage_writer_settlement() SET search_path=%I,pg_catalog,pg_temp',owner_schema,owner_schema); END $$;

CREATE FUNCTION nerocd_appdb_storage_writer_defer() RETURNS void LANGUAGE plpgsql AS $$
BEGIN
 SET CONSTRAINTS hosting_appdb_storage_writer_settlement DEFERRED;
END $$;
DO $$ DECLARE owner_schema text:=current_schema(); BEGIN EXECUTE format('ALTER FUNCTION %I.nerocd_appdb_storage_writer_defer() SET search_path=%I,pg_catalog,pg_temp',owner_schema,owner_schema); END $$;

-- A recovery association is authority only when the same transaction creates its
-- exact internal task and owner audit, bound to the retained closed failure.
CREATE FUNCTION nerocd_appdb_storage_recovery_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE op hosting_operations; writer hosting_application_database_storage_writers; stage hosting_application_database_stages; continuation hosting_application_database_route_continuations; failed hosting_proxy_operations;
BEGIN
 SELECT * INTO STRICT op FROM hosting_operations WHERE id=NEW.operation_id;
 SELECT * INTO STRICT writer FROM hosting_application_database_storage_writers WHERE operation_id=op.id AND phase=CASE NEW.action WHEN 'resume_predecessor' THEN 'replacement_failed' ELSE 'stopped' END;
 SELECT * INTO STRICT stage FROM hosting_application_database_stages WHERE operation_id=op.id ORDER BY CASE phase WHEN 'uncertain' THEN 7 WHEN 'ready' THEN 6 WHEN 'attached' THEN 5 WHEN 'network_configured' THEN 4 WHEN 'attach_intent' THEN 3 ELSE 1 END DESC LIMIT 1;
 IF NEW.request->>'operation_id' IS DISTINCT FROM op.id OR NEW.request->>'action' IS DISTINCT FROM NEW.action OR NEW.request->>'storage_state_hash' IS DISTINCT FROM writer.request->'receipt'->>'hash' OR NEW.request->>'stage_hash' IS DISTINCT FROM stage.stage_hash OR NEW.request->>'candidate_hash' IS DISTINCT FROM stage.stage->'candidate'->>'hash' OR NEW.body->>'version' IS DISTINCT FROM 'application-database.storage.v1' OR NEW.body->>'operation_id' IS DISTINCT FROM op.id OR NEW.body->>'action' IS DISTINCT FROM NEW.action OR NEW.body->>'run_id' IS DISTINCT FROM NEW.run_id OR NEW.body->>'request_hash' IS DISTINCT FROM NEW.request_hash OR NEW.body->>'storage_state_hash' IS DISTINCT FROM NEW.request->>'storage_state_hash' OR NEW.body->'storage_ref' IS DISTINCT FROM (SELECT body->'storage_ref' FROM hosting_application_database_storage WHERE operation_id=op.id) OR NOT EXISTS(SELECT 1 FROM hosting_binding_state WHERE hold_operation_id=op.id) OR NOT EXISTS(SELECT 1 FROM hosting_database_network_holds WHERE operation_id=op.id) OR EXISTS(SELECT 1 FROM run_leases WHERE run_id=op.run_id AND status='active' AND expires_at>clock_timestamp()) OR EXISTS(SELECT 1 FROM run_leases WHERE run_id='finalize_'||op.id) THEN RAISE EXCEPTION 'storage recovery association lacks exact retained authority' USING ERRCODE='23514'; END IF;
 IF NOT EXISTS(SELECT 1 FROM audit_events WHERE actor_id=NEW.actor_id AND action='hosting.application_database.storage_recovery_requested' AND target_id=op.id AND metadata->>'request_hash'=NEW.request_hash AND metadata->>'recovery_run_id'=NEW.run_id)  THEN RAISE EXCEPTION 'storage recovery requires its exact queued task and owner audit' USING ERRCODE='23514'; END IF;
 IF NEW.action='resume_predecessor' THEN
  IF op.state<>'failed' OR NOT EXISTS(SELECT 1 FROM task_runs WHERE id=NEW.run_id AND requested_by=NEW.actor_id AND project_id=op.project_id AND status='queued') OR EXISTS(SELECT 1 FROM hosting_application_database_yields WHERE operation_id=op.id) OR NOT EXISTS(SELECT 1 FROM hosting_task_targets WHERE run_id=NEW.run_id AND operation_id=op.id AND mode='storage_recover') THEN RAISE EXCEPTION 'storage resume requires pre-yield closed failure' USING ERRCODE='23514'; END IF;
 ELSE
  SELECT * INTO STRICT continuation FROM hosting_application_database_route_continuations WHERE operation_id=op.id;
  SELECT * INTO STRICT failed FROM hosting_proxy_operations WHERE id=continuation.proxy_operation_id;
  IF op.state NOT IN('applying','verifying') OR failed.state<>'refused' OR EXISTS(SELECT 1 FROM hosting_proxies WHERE hold_operation_id=failed.id) OR EXISTS(SELECT 1 FROM hosting_proxy_restore_holds WHERE proxy_id=failed.proxy_id) OR (EXISTS(SELECT 1 FROM audit_events WHERE target_id=failed.id AND action='hosting.route_switch_metadata_conflict') AND NOT EXISTS(SELECT 1 FROM hosting_proxy_recovery_resolutions WHERE operation_id=failed.id)) OR NOT EXISTS(SELECT 1 FROM hosting_proxy_receipts WHERE operation_id=failed.id AND phase='complete' AND body->>'outcome'='refused') OR NOT EXISTS(SELECT 1 FROM hosting_application_database_yields WHERE operation_id=op.id) OR NOT EXISTS(SELECT 1 FROM hosting_proxy_operations WHERE id=NEW.proxy_operation_id AND run_id IS NULL AND state='queued') THEN RAISE EXCEPTION 'storage forward recovery requires exact closed proxy refusal' USING ERRCODE='23514'; END IF;
 END IF;
 RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER hosting_appdb_storage_recovery_admission AFTER INSERT ON hosting_application_database_storage_recoveries DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION nerocd_appdb_storage_recovery_guard();
DO $$ DECLARE owner_schema text:=current_schema(); BEGIN EXECUTE format('ALTER FUNCTION %I.nerocd_appdb_storage_recovery_guard() SET search_path=%I,pg_catalog,pg_temp',owner_schema,owner_schema); END $$;
