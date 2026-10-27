CREATE TABLE hosting_stack_stop_plans (
 operation_id text PRIMARY KEY REFERENCES hosting_operations(id) ON DELETE RESTRICT,
 target_operation_id text NOT NULL REFERENCES hosting_operations(id) ON DELETE RESTRICT,
 plan jsonb NOT NULL CHECK(jsonb_typeof(plan)='object' AND plan->>'compiler_version'='managed-compose.stop.v1'),
 plan_hash text NOT NULL CHECK(plan_hash ~ '^sha256:[a-f0-9]{64}$'),
 CHECK(operation_id<>target_operation_id)
);
CREATE TRIGGER hosting_stack_stop_plans_immutable BEFORE UPDATE OR DELETE ON hosting_stack_stop_plans FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_execution_immutable();
CREATE TABLE hosting_stack_lifecycle (
 binding_id text PRIMARY KEY REFERENCES resource_bindings(id) ON DELETE RESTRICT,
 stopped jsonb CHECK(stopped IS NULL OR (jsonb_typeof(stopped)='object' AND stopped ? 'stop_operation_id' AND stopped ? 'target'))
);
ALTER TABLE hosting_task_targets DROP CONSTRAINT hosting_task_targets_mode_check;
ALTER TABLE hosting_task_targets ADD CONSTRAINT hosting_task_targets_mode_check CHECK(mode IN('deploy','observe','cleanup','finalize','storage_recover','stack_stop'));
CREATE UNIQUE INDEX hosting_one_stack_stop_task ON hosting_task_targets(operation_id) WHERE mode='stack_stop';

CREATE OR REPLACE FUNCTION nerocd_hosting_compose_lease() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE snap jsonb; target hosting_task_targets; spec jsonb;
BEGIN
 SELECT * INTO target FROM hosting_task_targets WHERE run_id=NEW.run_id;
 SELECT body INTO snap FROM hosting_snapshots WHERE operation_id=target.operation_id;
 IF snap ? 'stack' THEN
  SELECT run_spec INTO spec FROM task_runs WHERE id=NEW.run_id;
  IF target.runner_id IS DISTINCT FROM NEW.runner_id OR target.mode NOT IN('deploy','observe','stack_stop')
   OR spec->>'type' IS DISTINCT FROM 'image_deploy' OR spec->>'hosting_operation_id' IS DISTINCT FROM target.operation_id
   OR spec->>'hosting_mode' IS DISTINCT FROM target.mode OR spec-'type'-'hosting_operation_id'-'hosting_mode'-'inputs'<>'{}'::jsonb
   OR coalesce(spec->'inputs','{}'::jsonb)<>'{}'::jsonb THEN RAISE EXCEPTION 'invalid managed stack task' USING ERRCODE='23514'; END IF;
  PERFORM id FROM runners WHERE id=NEW.runner_id AND status='active' AND token_hash<>''
   AND capabilities @> ARRAY['hosting_managed_compose_v1','hosting_runtime_env_v1','image_release_ports_v1','image_deploy']::text[] AND NOT('shell'=ANY(capabilities))
   AND ((target.mode<>'stack_stop' AND NOT(snap ? 'stack_stopped_operation_id') AND NOT EXISTS(SELECT 1 FROM hosting_stack_stop_plans WHERE operation_id=target.operation_id)) OR 'hosting_stack_lifecycle_v1'=ANY(capabilities)) FOR SHARE;
  IF NOT FOUND THEN RAISE EXCEPTION 'managed stack requires versioned configured agent' USING ERRCODE='23514'; END IF;
  IF target.mode='stack_stop' AND NOT EXISTS(SELECT 1 FROM hosting_stack_stop_plans WHERE operation_id=target.operation_id) THEN RAISE EXCEPTION 'stack stop requires immutable target' USING ERRCODE='23514'; END IF;
 END IF;
 RETURN NEW;
END;$$;

CREATE FUNCTION nerocd_stack_stop_plan_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE stop_row hosting_operations; deployed hosting_operations; target_plan jsonb; healthy jsonb; snapshot jsonb;
BEGIN
 SELECT * INTO stop_row FROM hosting_operations WHERE id=NEW.operation_id;
 SELECT * INTO deployed FROM hosting_operations WHERE id=NEW.target_operation_id;
 SELECT body INTO target_plan FROM hosting_apply_plans WHERE operation_id=NEW.target_operation_id;
 SELECT body INTO snapshot FROM hosting_snapshots WHERE operation_id=NEW.operation_id;
 SELECT s.healthy INTO healthy FROM hosting_binding_state s WHERE s.binding_id=stop_row.binding_id;
 IF stop_row.resource_id IS DISTINCT FROM deployed.resource_id OR stop_row.binding_id IS DISTINCT FROM deployed.binding_id
 OR deployed.state IS DISTINCT FROM 'succeeded' OR NEW.plan->'target' IS DISTINCT FROM healthy
 OR NEW.plan->'target_plan' IS DISTINCT FROM target_plan OR NEW.plan->'target'->>'operation_id' IS DISTINCT FROM NEW.target_operation_id
 OR NEW.plan->'target'->'snapshot' IS DISTINCT FROM snapshot OR NEW.plan->>'hash' IS DISTINCT FROM NEW.plan_hash
 THEN RAISE EXCEPTION 'stack stop requires exact deployed receipt' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END;$$;
CREATE TRIGGER hosting_stack_stop_plan_guard BEFORE INSERT ON hosting_stack_stop_plans FOR EACH ROW EXECUTE FUNCTION nerocd_stack_stop_plan_guard();

CREATE FUNCTION nerocd_stack_lifecycle_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE held text; stop_id text; target jsonb;
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'stack lifecycle identity is retained' USING ERRCODE='23514'; END IF;
 IF TG_OP='UPDATE' AND NEW.binding_id IS DISTINCT FROM OLD.binding_id THEN RAISE EXCEPTION 'stack lifecycle binding changed' USING ERRCODE='23514'; END IF;
 SELECT hold_operation_id INTO held FROM hosting_binding_state WHERE binding_id=NEW.binding_id;
 IF NEW.stopped IS NOT NULL THEN
  stop_id:=NEW.stopped->>'stop_operation_id';
  SELECT p.plan->'target' INTO target FROM hosting_stack_stop_plans p JOIN hosting_operations o ON o.id=p.operation_id
   WHERE p.operation_id=stop_id AND o.binding_id=NEW.binding_id AND o.state='succeeded'
   AND (EXISTS(SELECT 1 FROM hosting_receipts r WHERE r.operation_id=o.id AND r.phase='complete' AND r.body->>'stack_stopped'='true')
    OR EXISTS(SELECT 1 FROM hosting_reconciliation_receipts r WHERE r.operation_id=o.id AND r.body->>'stack_stopped'='true'));
  IF held IS DISTINCT FROM stop_id OR target IS NULL OR NEW.stopped->'target' IS DISTINCT FROM target THEN RAISE EXCEPTION 'stopped stack requires exact completed removal' USING ERRCODE='23514'; END IF;
 ELSIF TG_OP='UPDATE' AND OLD.stopped IS NOT NULL THEN
  IF NOT EXISTS(SELECT 1 FROM hosting_operations o JOIN hosting_snapshots s ON s.operation_id=o.id WHERE o.id=held AND o.binding_id=NEW.binding_id AND o.state='succeeded' AND s.body->>'stack_stopped_operation_id'=OLD.stopped->>'stop_operation_id')
  THEN RAISE EXCEPTION 'stopped stack requires exact successful successor' USING ERRCODE='23514'; END IF;
 END IF;
 RETURN NEW;
END;$$;
CREATE TRIGGER hosting_stack_lifecycle_guard BEFORE INSERT OR UPDATE OR DELETE ON hosting_stack_lifecycle FOR EACH ROW EXECUTE FUNCTION nerocd_stack_lifecycle_guard();
