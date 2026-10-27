-- Extend the existing logical artifact and receipt authority to explicit stack components.
CREATE OR REPLACE FUNCTION nerocd_database_backup_admission() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE original hosting_database_backups; child hosting_operations; source hosting_operations; destination_key text;
BEGIN
 -- Use the existing destination admission lock before the stable role binding
 -- row. Both role admissions serialize even when issued as direct SQL. The
 -- following reads therefore see a competing admission after its commit.
 FOR destination_key IN SELECT DISTINCT destination_id FROM resource_bindings WHERE id IN (NEW.source_binding_id,NEW.target_binding_id) ORDER BY destination_id LOOP
  PERFORM pg_advisory_xact_lock(hashtextextended('nerocd-hosting-destination:'||destination_key,0));
 END LOOP;
 PERFORM 1 FROM hosting_binding_state WHERE binding_id=CASE WHEN NEW.mode='backup' THEN NEW.source_binding_id ELSE NEW.target_binding_id END FOR UPDATE;
 IF NOT FOUND THEN RAISE EXCEPTION 'database backup role binding unavailable' USING ERRCODE='23514'; END IF;
 IF (NEW.mode='backup' AND EXISTS(SELECT 1 FROM hosting_database_backups b WHERE b.ownership_held AND b.target_binding_id=NEW.source_binding_id)) OR
    (NEW.mode='restore' AND EXISTS(SELECT 1 FROM hosting_database_backups b WHERE b.ownership_held AND b.mode='backup' AND b.source_binding_id=NEW.target_binding_id)) THEN
  RAISE EXCEPTION 'database backup binding role already owned' USING ERRCODE='23514';
 END IF;
 SELECT * INTO source FROM hosting_operations WHERE id=NEW.plan->'source'->>'operation_id';
 IF source.id IS NULL OR source.resource_id<>NEW.source_resource_id OR source.binding_id<>NEW.source_binding_id OR source.project_id<>NEW.project_id OR NEW.plan->>'actor_id' IS DISTINCT FROM NEW.created_by OR NEW.plan->>'runner_id' IS DISTINCT FROM NEW.runner_id OR NEW.plan->'operation'->>'id' IS DISTINCT FROM NEW.id OR NEW.plan->'operation'->>'backup_id' IS DISTINCT FROM NEW.backup_id OR NEW.plan->'operation'->>'mode' IS DISTINCT FROM NEW.mode OR NEW.plan->'source'->'snapshot'->>'resource_generation' IS DISTINCT FROM NEW.source_generation::text OR NEW.plan->>'location_id' IS DISTINCT FROM NEW.backup_id OR NEW.plan->>'hash' IS DISTINCT FROM NEW.plan_hash OR NEW.run_id IS NOT NULL OR NOT NEW.ownership_held OR NOT EXISTS(SELECT 1 FROM hosting_apply_plans p WHERE p.operation_id=source.id AND p.body=NEW.plan->'source_plan' AND p.body_hash=NEW.plan->'source'->>'plan_hash') THEN RAISE EXCEPTION 'database backup admission identity mismatch' USING ERRCODE='23514'; END IF;
 IF NEW.mode='backup' THEN
  IF NEW.state<>'queued' OR NEW.blob IS NOT NULL OR NOT EXISTS(SELECT 1 FROM hosting_binding_state b WHERE b.binding_id=NEW.source_binding_id AND b.healthy=NEW.plan->'source' AND b.hold_operation_id IS NULL AND b.restore_admitted) THEN RAISE EXCEPTION 'database backup source is not settled' USING ERRCODE='23514'; END IF;
 ELSE
  SELECT * INTO original FROM hosting_database_backups WHERE id=NEW.backup_id;
  SELECT * INTO child FROM hosting_operations WHERE id=NEW.deployment_operation_id;
  IF original.id IS NULL OR original.mode<>'backup' OR original.state<>'succeeded' OR original.ownership_held OR original.blob IS DISTINCT FROM NEW.blob OR original.project_id<>NEW.project_id OR original.source_resource_id<>NEW.source_resource_id OR original.source_binding_id<>NEW.source_binding_id OR original.runner_id<>NEW.runner_id OR NEW.state<>'waiting_target' OR child.id IS NULL OR child.resource_id IS DISTINCT FROM NEW.target_resource_id OR child.binding_id IS DISTINCT FROM NEW.target_binding_id OR (NEW.plan->'operation'->>'version'<>'postgresql.compose-logical-local.v1' AND child.created_by<>NEW.created_by) OR child.project_id<>NEW.project_id OR NEW.plan->'source' IS DISTINCT FROM original.plan->'source' OR NEW.plan->'source_plan' IS DISTINCT FROM original.plan->'source_plan' OR NEW.plan->'source_storage' IS DISTINCT FROM original.plan->'source_storage' OR NEW.plan->'credentials' IS DISTINCT FROM original.plan->'credentials' THEN RAISE EXCEPTION 'database restore provenance or child mismatch' USING ERRCODE='23514'; END IF;
 END IF;
 IF NEW.plan->'operation'->>'version'='postgresql.compose-logical-local.v1' THEN
  IF EXISTS(SELECT 1 FROM hosting_operations o WHERE o.binding_id=CASE WHEN NEW.mode='backup' THEN NEW.source_binding_id ELSE NEW.target_binding_id END AND o.state NOT IN ('succeeded','failed','canceled')) OR NEW.plan->'component'->>'source' IS DISTINCT FROM NEW.plan->'operation'->>'source_component'
   OR NEW.plan->'operation'->>'source_deployed_operation_id' IS DISTINCT FROM source.id
   OR NOT(NEW.plan->'source' ? 'stack') OR NOT(NEW.plan->'source_plan' ? 'stack')
   OR NEW.plan->'source'->>'container_id' IS DISTINCT FROM ''
   OR NOT EXISTS(SELECT 1 FROM runners r WHERE r.id=NEW.runner_id AND r.status='active' AND r.token_hash<>'' AND r.capabilities @> ARRAY['hosting_compose_postgresql_backup_v1','hosting_managed_compose_v1','hosting_postgresql_backup_v1']::text[] AND NOT('shell'=ANY(r.capabilities)))
  THEN RAISE EXCEPTION 'component backup requires exact stack and configured agent' USING ERRCODE='23514'; END IF;
  IF NEW.mode='restore' AND (
   NEW.plan->'component'->>'target' IS DISTINCT FROM NEW.plan->'operation'->>'target_component'
   OR NEW.plan->'operation'->>'target_deployed_operation_id' IS DISTINCT FROM child.id
   OR child.state IS DISTINCT FROM 'succeeded'
   OR NEW.plan->'target'->>'operation_id' IS DISTINCT FROM child.id
   OR NEW.plan->'target'->'snapshot'->>'destination_id' IS DISTINCT FROM NEW.plan->'source'->'snapshot'->>'destination_id'
   OR NEW.plan->'target'->'snapshot'->>'runner_id' IS DISTINCT FROM NEW.runner_id
   OR NOT EXISTS(SELECT 1 FROM hosting_apply_plans p JOIN hosting_binding_state b ON b.binding_id=NEW.target_binding_id WHERE p.operation_id=child.id AND p.body=NEW.plan->'target_plan' AND b.healthy=NEW.plan->'target' AND b.hold_operation_id IS NULL AND b.restore_admitted)
  ) THEN RAISE EXCEPTION 'component restore target is not exact settled stack' USING ERRCODE='23514'; END IF;
 ELSIF NEW.plan ? 'component' OR NEW.plan->'operation'->>'version' IS DISTINCT FROM 'postgresql.logical-local.v1' THEN
  RAISE EXCEPTION 'unsupported database backup plan version' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;

CREATE OR REPLACE FUNCTION nerocd_database_backup_receipt() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE b hosting_database_backups; l run_leases; a jsonb:=NEW.authority; extra text[]:=ARRAY['receipt_id','plan_hash','phase','blob','failure_code','local_copy']; intent jsonb;
BEGIN
 SELECT * INTO b FROM hosting_database_backups WHERE id=NEW.operation_id;
 SELECT * INTO l FROM run_leases WHERE id=a->>'lease_id';
 IF b.plan->'operation'->>'version'='postgresql.compose-logical-local.v1' THEN
  extra:=extra || ARRAY['component_version','writers','writers_resumed'];
  IF NEW.body->>'component_version' IS DISTINCT FROM 'postgresql.compose-logical-local.v1'
   OR (NEW.phase='complete' AND NEW.body->>'writers_resumed' IS DISTINCT FROM 'true')
  THEN RAISE EXCEPTION 'component backup receipt version or resume mismatch' USING ERRCODE='23514'; END IF;
  IF b.mode='restore' AND NEW.phase IN ('complete','uncertain') THEN
   SELECT body INTO intent FROM hosting_database_backup_receipts WHERE operation_id=b.id AND phase='intent';
   IF intent IS NULL OR NEW.body->'writers' IS DISTINCT FROM intent->'writers' THEN RAISE EXCEPTION 'component import writer snapshot changed' USING ERRCODE='23514'; END IF;
  END IF;
 END IF;
 IF b.id IS NULL OR l.id IS NULL OR b.run_id IS DISTINCT FROM l.run_id OR b.runner_id IS DISTINCT FROM l.runner_id OR a->>'operation_id' IS DISTINCT FROM b.id OR a->>'run_id' IS DISTINCT FROM b.run_id OR a->>'attempt' IS DISTINCT FROM l.attempt::text OR a->>'fence' IS DISTINCT FROM l.fence OR NEW.body->>'receipt_id' IS DISTINCT FROM NEW.receipt_id OR NEW.body->>'plan_hash' IS DISTINCT FROM b.plan_hash OR NEW.body->>'phase' IS DISTINCT FROM NEW.phase OR NEW.body - extra IS DISTINCT FROM a OR l.expires_at<=clock_timestamp() OR NEW.response->>'id' IS DISTINCT FROM b.id OR NEW.response->>'state' IS DISTINCT FROM b.state OR NEW.response->'blob' IS DISTINCT FROM b.blob THEN RAISE EXCEPTION 'database backup receipt authority mismatch' USING ERRCODE='23514'; END IF;
 IF (NEW.phase='intent' AND (b.state<>'applying' OR l.status<>'active' OR NOT b.ownership_held)) OR (NEW.phase='complete' AND (b.state<>'succeeded' OR l.status<>'succeeded' OR b.ownership_held OR NEW.body->'blob' IS DISTINCT FROM b.blob)) OR (NEW.phase='failed' AND (b.state<>'failed' OR l.status<>'failed' OR b.ownership_held IS DISTINCT FROM (b.mode='restore'))) OR (NEW.phase='uncertain' AND (b.state<>'uncertain' OR l.status<>'failed' OR NOT b.ownership_held)) THEN RAISE EXCEPTION 'database backup receipt settlement mismatch' USING ERRCODE='23514'; END IF;
 IF NEW.body ? 'local_copy' AND (b.mode<>'backup' OR NEW.phase<>'complete' OR NOT EXISTS(SELECT 1 FROM hosting_database_backup_copies c WHERE c.backup_id=b.id AND c.location='local' AND (c.body->'local'=NEW.body->'local_copy' OR (NEW.body->'local_copy'->>'created'='false' AND c.body->'local'=jsonb_set(NEW.body->'local_copy','{created}','true'::jsonb))) AND c.body->'item'->>'digest'=b.blob->>'digest')) THEN RAISE EXCEPTION 'database backup copy proof mismatch' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;


-- A component import owns the existing target binding; it does not create a
-- database deployment child or alter the legacy standalone recipe boundary.
CREATE FUNCTION nerocd_component_backup_stack_hold() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE destination_key text;
BEGIN
 SELECT destination_id INTO destination_key FROM resource_bindings WHERE id=NEW.binding_id;
 PERFORM pg_advisory_xact_lock(hashtextextended('nerocd-hosting-destination:'||destination_key,0));
 PERFORM 1 FROM hosting_binding_state WHERE binding_id=NEW.binding_id FOR UPDATE;
 IF EXISTS(SELECT 1 FROM hosting_database_backups b WHERE b.ownership_held
  AND b.plan->'operation'->>'version'='postgresql.compose-logical-local.v1'
  AND ((b.mode='backup' AND b.source_binding_id=NEW.binding_id) OR b.target_binding_id=NEW.binding_id))
 THEN RAISE EXCEPTION 'stack owned by component database backup or import' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER hosting_component_backup_stack_hold BEFORE INSERT ON hosting_operations FOR EACH ROW EXECUTE FUNCTION nerocd_component_backup_stack_hold();

CREATE FUNCTION nerocd_component_backup_lease() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE b hosting_database_backups;
BEGIN
 SELECT * INTO b FROM hosting_database_backups WHERE run_id=NEW.run_id;
 IF b.plan->'operation'->>'version'='postgresql.compose-logical-local.v1' THEN
  PERFORM 1 FROM runners WHERE id=NEW.runner_id AND status='active' AND token_hash<>''
   AND capabilities @> ARRAY['hosting_compose_postgresql_backup_v1','hosting_managed_compose_v1','hosting_postgresql_backup_v1']::text[] AND NOT('shell'=ANY(capabilities)) FOR SHARE;
  IF NOT FOUND THEN RAISE EXCEPTION 'component backup requires versioned configured agent' USING ERRCODE='23514'; END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER hosting_component_backup_lease BEFORE INSERT ON run_leases FOR EACH ROW EXECUTE FUNCTION nerocd_component_backup_lease();
