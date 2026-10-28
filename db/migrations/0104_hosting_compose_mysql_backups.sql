-- Extend the existing component protocol; preserve PostgreSQL and native MySQL guards.
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
  IF original.id IS NULL OR original.mode<>'backup' OR original.state<>'succeeded' OR original.ownership_held OR original.blob IS DISTINCT FROM NEW.blob OR original.project_id<>NEW.project_id OR original.source_resource_id<>NEW.source_resource_id OR original.source_binding_id<>NEW.source_binding_id OR original.runner_id<>NEW.runner_id OR NEW.state<>'waiting_target' OR child.id IS NULL OR child.resource_id IS DISTINCT FROM NEW.target_resource_id OR child.binding_id IS DISTINCT FROM NEW.target_binding_id OR (NEW.plan->'operation'->>'version' NOT IN ('postgresql.compose-logical-local.v1','mysql.compose-logical-local.v1') AND child.created_by<>NEW.created_by) OR child.project_id<>NEW.project_id OR NEW.plan->'source' IS DISTINCT FROM original.plan->'source' OR NEW.plan->'source_plan' IS DISTINCT FROM original.plan->'source_plan' OR NEW.plan->'source_storage' IS DISTINCT FROM original.plan->'source_storage' OR NEW.plan->'credentials' IS DISTINCT FROM original.plan->'credentials' THEN RAISE EXCEPTION 'database restore provenance or child mismatch' USING ERRCODE='23514'; END IF;
 END IF;
 IF NEW.plan->'operation'->>'version' IN ('postgresql.compose-logical-local.v1','mysql.compose-logical-local.v1') THEN
  IF EXISTS(SELECT 1 FROM hosting_operations o WHERE o.binding_id=CASE WHEN NEW.mode='backup' THEN NEW.source_binding_id ELSE NEW.target_binding_id END AND o.state NOT IN ('succeeded','failed','canceled')) OR NEW.plan->'component'->>'source' IS DISTINCT FROM NEW.plan->'operation'->>'source_component'
   OR NEW.plan->'operation'->>'source_deployed_operation_id' IS DISTINCT FROM source.id
   OR NOT(NEW.plan->'source' ? 'stack') OR NOT(NEW.plan->'source_plan' ? 'stack')
   OR NEW.plan->'source'->>'container_id' IS DISTINCT FROM ''
   OR NOT EXISTS(SELECT 1 FROM runners r WHERE r.id=NEW.runner_id AND r.status='active' AND r.token_hash<>'' AND r.capabilities @> ARRAY[(CASE WHEN NEW.plan->'operation'->>'version'='mysql.compose-logical-local.v1' THEN 'hosting_compose_mysql_backup_v1' ELSE 'hosting_compose_postgresql_backup_v1' END),'hosting_managed_compose_v1',(CASE WHEN NEW.plan->'operation'->>'version'='mysql.compose-logical-local.v1' THEN 'hosting_mysql_backup_v1' ELSE 'hosting_postgresql_backup_v1' END)]::text[] AND NOT('shell'=ANY(r.capabilities)))
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
 ELSIF NEW.plan ? 'component' OR coalesce(NEW.plan->'operation'->>'version','') NOT IN('postgresql.logical-local.v1','mysql.logical-local.v1') THEN
  RAISE EXCEPTION 'unsupported database backup plan version' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;

CREATE OR REPLACE FUNCTION nerocd_database_backup_receipt() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE b hosting_database_backups; l run_leases; a jsonb:=NEW.authority; extra text[]:=ARRAY['receipt_id','plan_hash','phase','blob','failure_code','local_copy']; intent jsonb;
BEGIN
 SELECT * INTO b FROM hosting_database_backups WHERE id=NEW.operation_id;
 SELECT * INTO l FROM run_leases WHERE id=a->>'lease_id';
 IF b.plan->'operation'->>'version' IN ('postgresql.compose-logical-local.v1','mysql.compose-logical-local.v1') THEN
  extra:=extra || ARRAY['component_version','writers','writers_resumed'];
  IF NEW.body->>'component_version' IS DISTINCT FROM b.plan->'operation'->>'version'
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

CREATE OR REPLACE FUNCTION nerocd_component_backup_stack_hold() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE destination_key text;
BEGIN
 SELECT destination_id INTO destination_key FROM resource_bindings WHERE id=NEW.binding_id;
 PERFORM pg_advisory_xact_lock(hashtextextended('nerocd-hosting-destination:'||destination_key,0));
 PERFORM 1 FROM hosting_binding_state WHERE binding_id=NEW.binding_id FOR UPDATE;
 IF EXISTS(SELECT 1 FROM hosting_database_backups b WHERE b.ownership_held
  AND b.plan->'operation'->>'version' IN ('postgresql.compose-logical-local.v1','mysql.compose-logical-local.v1')
  AND ((b.mode='backup' AND b.source_binding_id=NEW.binding_id) OR b.target_binding_id=NEW.binding_id))
 THEN RAISE EXCEPTION 'stack owned by component database backup or import' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;

CREATE OR REPLACE FUNCTION nerocd_component_backup_lease() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE b hosting_database_backups;
BEGIN
 SELECT * INTO b FROM hosting_database_backups WHERE run_id=NEW.run_id;
 IF b.plan->'operation'->>'version' IN ('postgresql.compose-logical-local.v1','mysql.compose-logical-local.v1') THEN
  PERFORM 1 FROM runners WHERE id=NEW.runner_id AND status='active' AND token_hash<>''
   AND capabilities @> ARRAY[(CASE WHEN b.plan->'operation'->>'version'='mysql.compose-logical-local.v1' THEN 'hosting_compose_mysql_backup_v1' ELSE 'hosting_compose_postgresql_backup_v1' END),'hosting_managed_compose_v1',(CASE WHEN b.plan->'operation'->>'version'='mysql.compose-logical-local.v1' THEN 'hosting_mysql_backup_v1' ELSE 'hosting_postgresql_backup_v1' END)]::text[] AND NOT('shell'=ANY(capabilities)) FOR SHARE;
  IF NOT FOUND THEN RAISE EXCEPTION 'component backup requires versioned configured agent' USING ERRCODE='23514'; END IF;
 END IF;
 RETURN NEW;
END $$;

CREATE OR REPLACE FUNCTION nerocd_database_backup_lease() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE b hosting_database_backups; r runners; spec jsonb;
BEGIN
 SELECT run_spec INTO spec FROM task_runs WHERE id=NEW.run_id;
 IF spec->>'type' IS DISTINCT FROM 'database_backup' THEN RETURN NEW; END IF;
 SELECT * INTO b FROM hosting_database_backups WHERE run_id=NEW.run_id;
 SELECT * INTO r FROM runners WHERE id=NEW.runner_id;
 IF b.id IS NULL OR b.runner_id<>r.id OR NOT b.ownership_held OR b.state<>'dispatched' OR r.status<>'active' OR r.token_hash='' OR NOT('database_backup'=ANY(r.capabilities)) OR NOT((CASE WHEN b.plan->'operation'->>'version' IN ('mysql.logical-local.v1','mysql.compose-logical-local.v1') THEN 'hosting_mysql_backup_v1' ELSE 'hosting_postgresql_backup_v1' END)=ANY(r.capabilities)) OR (b.plan->'operation'->>'version'='mysql.logical-local.v1' AND NOT('hosting_mysql_v1'=ANY(r.capabilities))) OR 'shell'=ANY(r.capabilities) OR spec<>jsonb_build_object('type','database_backup','inputs','{}'::jsonb,'hosting_operation_id',b.id,'hosting_mode',b.mode) OR EXISTS(SELECT 1 FROM run_leases WHERE run_id=NEW.run_id) THEN RAISE EXCEPTION 'database backup lease identity mismatch' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;

CREATE OR REPLACE FUNCTION nerocd_mysql_backup_consistent() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE engine_key text; version_key text; format_key text; selected jsonb;
BEGIN
 version_key:=NEW.plan->'operation'->>'version';
 IF version_key='mysql.compose-logical-local.v1' THEN
  FOR selected IN SELECT NEW.plan->'source_plan' UNION ALL SELECT NEW.plan->'target_plan' WHERE NEW.mode='restore' LOOP
   IF selected->'stack'->'definition'->>'public_component' IS NOT DISTINCT FROM 'mysql'
    OR NOT EXISTS(SELECT 1 FROM jsonb_array_elements(selected->'stack'->'images') image WHERE image->>'component'='mysql' AND image->'release'->>'canonical_image'='docker.io/library/mysql@sha256:a0225d1ea6631c8bc2385dc4c2faba17bbf1834ecf369e1659a9a382bd8d5dd7')
    OR NOT EXISTS(SELECT 1 FROM jsonb_array_elements(selected->'stack'->'definition'->'services') service WHERE service->>'name'='mysql' AND (service->'command' IS NULL OR service->'command'='null'::jsonb) AND service->'environment'->>'MYSQL_DATABASE'='wordpress' AND service->'environment'->>'MYSQL_ROOT_PASSWORD'='${SERVICE_PASSWORD_ROOT:?Supply a separate managed MySQL root password reference}' AND service->'mounts'='[{"volume":"mysql-data","target":"/var/lib/mysql"}]'::jsonb)
    OR EXISTS(SELECT 1 FROM jsonb_array_elements(selected->'stack'->'definition'->'services') service, jsonb_array_elements(service->'mounts') mount WHERE service->>'name'<>'mysql' AND mount->>'volume'='mysql-data')
   THEN RAISE EXCEPTION 'unsupported MySQL component profile' USING ERRCODE='23514'; END IF;
  END LOOP;
  IF NEW.plan->'operation' ? 'remote_target' OR EXISTS(SELECT 1 FROM hosting_database_backup_remote_selections WHERE operation_id=NEW.id) OR (NEW.blob IS NOT NULL AND NEW.blob->>'format' IS DISTINCT FROM 'mysql_sql') OR NEW.plan->'component'->>'source' IS DISTINCT FROM 'mysql' THEN RAISE EXCEPTION 'MySQL component local archive identity mismatch' USING ERRCODE='23514'; END IF;
  RETURN NEW;
 END IF;
 IF version_key='postgresql.compose-logical-local.v1' THEN RETURN NEW; END IF;
 SELECT engine INTO STRICT engine_key FROM hosting_databases WHERE resource_id=NEW.source_resource_id;
 IF engine_key='mysql' THEN version_key:='mysql.logical-local.v1';format_key:='mysql_sql';
 ELSIF engine_key='postgresql' THEN version_key:='postgresql.logical-local.v1';format_key:='postgresql_custom';
 ELSE RAISE EXCEPTION 'unsupported native backup engine' USING ERRCODE='23514'; END IF;
 IF NEW.plan->'operation'->>'version' IS DISTINCT FROM version_key OR NEW.plan->'source'->'snapshot'->'database'->>'engine' IS DISTINCT FROM engine_key OR (NEW.blob IS NOT NULL AND NEW.blob->>'format' IS DISTINCT FROM format_key) OR (NEW.plan->'operation' ? 'blob' AND NEW.plan->'operation'->'blob'->>'format' IS DISTINCT FROM format_key) THEN RAISE EXCEPTION 'native backup engine or archive format mismatch' USING ERRCODE='23514'; END IF;
 IF NEW.mode='restore' AND NOT EXISTS(SELECT 1 FROM hosting_databases target JOIN hosting_databases original ON original.resource_id=NEW.source_resource_id WHERE target.resource_id=NEW.target_resource_id AND target.engine=original.engine AND target.recipe_version=original.recipe_version) THEN RAISE EXCEPTION 'restore target engine mismatch' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;

CREATE FUNCTION nerocd_mysql_component_backup_schema_compatible() RETURNS boolean LANGUAGE sql STABLE AS $$ SELECT true $$;
DO $migration$
DECLARE owner_schema text:=current_schema(); routine text;
BEGIN
 FOREACH routine IN ARRAY ARRAY['nerocd_database_backup_admission()','nerocd_database_backup_receipt()','nerocd_component_backup_stack_hold()','nerocd_component_backup_lease()','nerocd_database_backup_lease()','nerocd_mysql_backup_consistent()','nerocd_mysql_component_backup_schema_compatible()'] LOOP
  EXECUTE format('ALTER FUNCTION %I.%s SET search_path TO pg_catalog, %I, pg_temp',owner_schema,routine,owner_schema);
 END LOOP;
END;$migration$;
