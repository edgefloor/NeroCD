-- Empty membership upgrades reuse database_deploy and existing owned storage.
-- The sidecar is historical proof, never a lease or permission to create data.
CREATE TABLE hosting_database_network_upgrades (
 operation_id text PRIMARY KEY REFERENCES hosting_database_deployments(operation_id) DEFERRABLE INITIALLY DEFERRED,
 capture_id text NOT NULL REFERENCES hosting_database_network_captures(id),
 origin_receipt_id text NOT NULL REFERENCES hosting_database_network_origin_receipts(receipt_id),
 previous_operation_id text NOT NULL REFERENCES hosting_operations(id),
 proof jsonb NOT NULL CHECK(jsonb_typeof(proof)='object' AND octet_length(proof::text)<=65536),
 proof_hash text NOT NULL CHECK(proof_hash ~ '^sha256:[0-9a-f]{64}$'),
 audit_id text NOT NULL UNIQUE REFERENCES audit_events(id) DEFERRABLE INITIALLY DEFERRED,
 CHECK(proof - 'version' - 'capture_plan' - 'origin_receipt' - 'previous_plan' - 'previous_healthy' - 'hash'='{}'::jsonb),
 CHECK((proof->>'version'='database-network.empty-upgrade.v1' AND proof->>'hash'=proof_hash AND proof->'capture_plan'->>'operation_id'=capture_id AND proof->'origin_receipt'->>'id'=origin_receipt_id AND proof->'previous_healthy'->>'operation_id'=previous_operation_id AND operation_id<>previous_operation_id) IS TRUE)
);
CREATE TRIGGER hosting_database_network_upgrades_immutable BEFORE UPDATE OR DELETE ON hosting_database_network_upgrades FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_runtime_immutable();

CREATE FUNCTION nerocd_database_network_upgrade_validate(operation_key text, admission boolean) RETURNS void LANGUAGE plpgsql AS $$
DECLARE u hosting_database_network_upgrades; c hosting_database_network_captures; r hosting_database_network_origin_receipts; o hosting_operations; prior hosting_operations; snap jsonb; old_plan jsonb; new_plan jsonb; expected_network jsonb; expected_predecessor jsonb; revision bigint; alias text;
BEGIN
 SELECT * INTO STRICT u FROM hosting_database_network_upgrades WHERE operation_id=operation_key;
 SELECT * INTO STRICT c FROM hosting_database_network_captures WHERE id=u.capture_id;
 SELECT * INTO STRICT r FROM hosting_database_network_origin_receipts WHERE capture_id=c.id AND receipt_id=u.origin_receipt_id;
 SELECT * INTO STRICT o FROM hosting_operations WHERE id=operation_key;
 SELECT * INTO STRICT prior FROM hosting_operations WHERE id=u.previous_operation_id;
 SELECT body INTO STRICT snap FROM hosting_snapshots WHERE operation_id=o.id;
 SELECT body INTO STRICT old_plan FROM hosting_apply_plans WHERE operation_id=prior.id;
 IF prior.state<>'succeeded' OR o.project_id<>c.project_id OR o.resource_id<>c.resource_id OR o.binding_id<>c.binding_id OR prior.project_id<>o.project_id OR prior.resource_id<>o.resource_id OR prior.binding_id<>o.binding_id OR u.proof->'capture_plan' IS DISTINCT FROM c.plan OR u.proof->'origin_receipt' IS DISTINCT FROM r.receipt OR u.proof->'previous_plan' IS DISTINCT FROM old_plan OR snap IS DISTINCT FROM old_plan->'snapshot' OR snap IS DISTINCT FROM c.plan->'source_plan'->'snapshot' OR old_plan->'release' IS DISTINCT FROM c.plan->'source_plan'->'release' OR old_plan->'storage' IS DISTINCT FROM c.plan->'source_plan'->'storage' OR old_plan->'database'-'network' IS DISTINCT FROM c.plan->'source_plan'->'database'-'network' OR u.proof->'previous_healthy'->'snapshot' IS DISTINCT FROM snap OR u.proof->'previous_healthy'->'release' IS DISTINCT FROM old_plan->'release' OR u.proof->'previous_healthy'->>'plan_hash' IS DISTINCT FROM old_plan->>'hash' OR u.proof->'previous_healthy'->'storage'->'plan' IS DISTINCT FROM old_plan->'storage' OR u.proof->'previous_healthy'->'storage'->>'creation_plan_hash' IS DISTINCT FROM c.plan->'materialization'->>'creation_plan_hash' THEN
  RAISE EXCEPTION 'database network upgrade proof association mismatch' USING ERRCODE='23514';
 END IF;
 IF NOT EXISTS(SELECT 1 FROM hosting_receipts p WHERE p.operation_id=prior.id AND p.phase='complete' AND p.body->>'outcome'='succeeded' AND p.body->>'ready'='true' AND p.body->>'container_id'=u.proof->'previous_healthy'->>'container_id' AND p.body->>'plan_hash'=old_plan->>'hash') OR NOT EXISTS(SELECT 1 FROM audit_events a WHERE a.id=u.audit_id AND a.actor_id=o.created_by AND a.target_id=o.id AND a.action='hosting.database_network_upgrade.requested' AND a.metadata->>'capture_id'=c.id AND a.metadata->>'proof_hash'=u.proof_hash) THEN
  RAISE EXCEPTION 'database network upgrade receipt or audit mismatch' USING ERRCODE='23514';
 END IF;
 alias:='ncdb-'||left(encode(sha256(convert_to('{"Binding":'||to_jsonb(o.binding_id)::text||'}','UTF8')),'hex'),32);
 IF old_plan->>'compiler_version'='database-hosting.private.owned-storage.v1' THEN
  IF old_plan IS DISTINCT FROM c.plan->'source_plan' OR u.proof->'previous_healthy' IS DISTINCT FROM c.plan->'source_healthy' THEN RAISE EXCEPTION 'database initial upgrade predecessor mismatch' USING ERRCODE='23514'; END IF;
  revision:=1;
  expected_predecessor:=jsonb_build_object('compiler_version',old_plan->>'compiler_version','plan_hash',old_plan->>'hash');
 ELSIF old_plan->>'compiler_version'='database-hosting.private.membership.v2' THEN
  IF old_plan->'database'->'network'->'members' IS DISTINCT FROM '[]'::jsonb OR old_plan->'database'->'network'->>'network_id' IS DISTINCT FROM r.receipt->'origin'->>'network_id' OR old_plan->'database'->'network'->>'origin_hash' IS DISTINCT FROM r.receipt->>'origin_hash' OR old_plan->'database'->'network'->>'alias' IS DISTINCT FROM alias THEN RAISE EXCEPTION 'database maintenance requires same empty origin' USING ERRCODE='23514'; END IF;
  revision:=(old_plan->'database'->'network'->>'membership_revision')::bigint;
  expected_predecessor:=jsonb_build_object('compiler_version',old_plan->>'compiler_version','plan_hash',old_plan->>'hash','alias',alias);
 ELSE RAISE EXCEPTION 'database upgrade predecessor family mismatch' USING ERRCODE='23514'; END IF;
 IF admission AND (u.proof->'previous_healthy' IS DISTINCT FROM (SELECT healthy FROM hosting_binding_state WHERE binding_id=o.binding_id) OR EXISTS(SELECT 1 FROM hosting_binding_state WHERE binding_id=o.binding_id AND (hold_operation_id IS NOT NULL OR NOT restore_admitted)) OR EXISTS(SELECT 1 FROM hosting_binding_restore_holds WHERE binding_id=o.binding_id)) THEN RAISE EXCEPTION 'database upgrade predecessor no longer current' USING ERRCODE='23514'; END IF;
 SELECT body INTO new_plan FROM hosting_apply_plans WHERE operation_id=o.id;
 IF FOUND THEN
  expected_network:=jsonb_build_object('version','database-network.private.membership.v2','network_id',r.receipt->'origin'->>'network_id','origin_hash',r.receipt->>'origin_hash','alias',alias,'membership_revision',revision,'members','[]'::jsonb,'predecessor',expected_predecessor);
  IF new_plan->>'compiler_version' IS DISTINCT FROM 'database-hosting.private.membership.v2' OR new_plan->'snapshot' IS DISTINCT FROM snap OR new_plan->'release' IS DISTINCT FROM old_plan->'release' OR new_plan->'storage' IS DISTINCT FROM old_plan->'storage' OR new_plan->'database'-'network' IS DISTINCT FROM old_plan->'database'-'network' OR new_plan->'previous_healthy' IS DISTINCT FROM u.proof->'previous_healthy' OR new_plan->'database'->'network'-'hash' IS DISTINCT FROM expected_network OR coalesce(new_plan->'retain_previous','false'::jsonb)<>'false'::jsonb OR new_plan ? 'application_database' OR coalesce(new_plan->'retained_releases','[]'::jsonb)<>'[]'::jsonb THEN RAISE EXCEPTION 'database upgrade plan substitution' USING ERRCODE='23514'; END IF;
 END IF;
END $$;

CREATE FUNCTION nerocd_database_network_upgrade_consistent() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
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
CREATE CONSTRAINT TRIGGER hosting_database_network_upgrade_consistency AFTER INSERT ON hosting_database_network_upgrades DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION nerocd_database_network_upgrade_consistent();
CREATE CONSTRAINT TRIGGER hosting_database_network_upgrade_snapshot AFTER INSERT ON hosting_snapshots DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION nerocd_database_network_upgrade_consistent();
CREATE CONSTRAINT TRIGGER hosting_database_network_upgrade_plan AFTER INSERT ON hosting_apply_plans DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION nerocd_database_network_upgrade_consistent();

-- A separate lease guard adds the upgrade capability without changing v1.
CREATE FUNCTION nerocd_database_network_upgrade_lease() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF EXISTS(SELECT 1 FROM hosting_task_targets t JOIN hosting_database_network_upgrades u ON u.operation_id=t.operation_id WHERE t.run_id=NEW.run_id) THEN
  PERFORM id FROM runners WHERE id=NEW.runner_id AND status='active' AND token_hash<>'' AND 'hosting_database_network_upgrade_v1'=ANY(capabilities) AND 'hosting_database_v1'=ANY(capabilities) AND 'hosting_owned_storage_v1'=ANY(capabilities) AND 'database_deploy'=ANY(capabilities) AND NOT('shell'=ANY(capabilities)) FOR SHARE;
  IF NOT FOUND THEN RAISE EXCEPTION 'database network upgrade capability required' USING ERRCODE='23514'; END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER hosting_database_network_upgrade_lease BEFORE INSERT ON run_leases FOR EACH ROW EXECUTE FUNCTION nerocd_database_network_upgrade_lease();
CREATE FUNCTION nerocd_database_network_upgrade_flush() RETURNS void LANGUAGE plpgsql AS $$ BEGIN SET CONSTRAINTS ALL IMMEDIATE; END $$;
CREATE FUNCTION nerocd_database_network_upgrade_schema_compatible() RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT NOT EXISTS(SELECT 1 FROM (VALUES('hosting_database_network_upgrades','hosting_database_network_upgrades_immutable'),('hosting_database_network_upgrades','hosting_database_network_upgrade_consistency'),('hosting_apply_plans','hosting_database_network_upgrade_plan'),('hosting_snapshots','hosting_database_network_upgrade_snapshot'),('run_leases','hosting_database_network_upgrade_lease')) required(table_name,trigger_name) WHERE NOT EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass(current_schema()||'.'||table_name) AND tgname=trigger_name AND tgenabled IN('O','A')));
$$;
DO $$ DECLARE owner_schema text:=current_schema(); routine text; BEGIN
 FOREACH routine IN ARRAY ARRAY['nerocd_database_network_upgrade_validate(text,boolean)','nerocd_database_network_upgrade_consistent()','nerocd_database_network_upgrade_lease()','nerocd_database_network_upgrade_flush()','nerocd_database_network_upgrade_schema_compatible()'] LOOP EXECUTE format('ALTER FUNCTION %I.%s SET search_path=%I,pg_catalog,pg_temp',owner_schema,routine,owner_schema); END LOOP;
END $$;
