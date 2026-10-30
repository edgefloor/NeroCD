-- Retention remains resource-scoped. A component scope is selected by the server
-- and cannot be reinterpreted after policy creation or across immutable previews.
CREATE FUNCTION nerocd_backup_retention_source_scope() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE selected jsonb; policy jsonb;
BEGIN
 selected:=NEW.body->'source_scope';
 IF TG_TABLE_NAME='hosting_database_backup_retention_policies' THEN
  IF TG_OP='UPDATE' AND (NEW.body->'source_scope' IS DISTINCT FROM OLD.body->'source_scope' OR NEW.body->>'version' IS DISTINCT FROM OLD.body->>'version') THEN
   RAISE EXCEPTION 'retention source scope is immutable' USING ERRCODE='23514';
  END IF;
  IF selected IS NOT NULL AND (selected IS DISTINCT FROM '{"backup_version":"mysql.compose-logical-local.v1","component":"mysql"}'::jsonb OR NEW.body->>'version' IS DISTINCT FROM 'mysql.backup-retention.v1') THEN
   RAISE EXCEPTION 'retention source scope invalid' USING ERRCODE='23514';
  END IF;
 ELSE
  SELECT body INTO policy FROM hosting_database_backup_retention_policies WHERE resource_id=NEW.resource_id AND revision=NEW.policy_revision;
  IF policy IS NULL OR selected IS DISTINCT FROM policy->'source_scope' OR NEW.body->>'version' IS DISTINCT FROM policy->>'version' THEN
   RAISE EXCEPTION 'retention preview source scope mismatch' USING ERRCODE='23514';
  END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER hosting_database_backup_retention_source_scope BEFORE INSERT OR UPDATE ON hosting_database_backup_retention_policies FOR EACH ROW EXECUTE FUNCTION nerocd_backup_retention_source_scope();
CREATE TRIGGER hosting_database_backup_retention_preview_scope BEFORE INSERT ON hosting_database_backup_retention_previews FOR EACH ROW EXECUTE FUNCTION nerocd_backup_retention_source_scope();

CREATE OR REPLACE FUNCTION nerocd_backup_retention_lease() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE e hosting_database_backup_retention_executions; r runners; spec jsonb;
BEGIN
 SELECT run_spec INTO spec FROM task_runs WHERE id=NEW.run_id;
 IF spec->>'type' IS DISTINCT FROM 'database_backup_retention' THEN RETURN NEW; END IF;
 SELECT * INTO e FROM hosting_database_backup_retention_executions WHERE run_id=NEW.run_id;
 SELECT * INTO r FROM runners WHERE id=NEW.runner_id;
 IF (e.plan->'operation'->>'version' IS DISTINCT FROM 'postgresql.backup-retention.v1' AND e.plan->'operation'->>'version' IS DISTINCT FROM 'mysql.backup-retention.v1') OR (e.plan->'operation'->>'version'='mysql.backup-retention.v1' AND NOT (EXISTS(SELECT 1 FROM hosting_databases d WHERE d.resource_id=e.resource_id AND d.engine='mysql' AND d.recipe_version='mysql-8.4.13-oraclelinux9.v1') OR EXISTS(
  SELECT 1 FROM hosting_database_backup_retention_previews p
  WHERE p.id=e.preview_id AND p.resource_id=e.resource_id AND p.policy_revision=(e.plan->'operation'->>'policy_revision')::bigint AND p.plan_hash=e.plan->'operation'->>'plan_hash'
   AND p.body->'source_scope'='{"backup_version":"mysql.compose-logical-local.v1","component":"mysql"}'::jsonb
   AND EXISTS(SELECT 1 FROM hosting_database_backup_retention_items i WHERE i.execution_id=e.id)
   AND NOT EXISTS(SELECT 1 FROM hosting_database_backup_retention_items i JOIN hosting_database_backup_copies c ON c.id=i.copy_id JOIN hosting_database_backups b ON b.id=c.backup_id WHERE i.execution_id=e.id AND (b.source_resource_id IS DISTINCT FROM e.resource_id OR b.plan->'operation'->>'version' IS DISTINCT FROM 'mysql.compose-logical-local.v1' OR b.plan->'component'->>'source' IS DISTINCT FROM 'mysql'))
 ))) THEN RAISE EXCEPTION 'retention engine identity mismatch' USING ERRCODE='23514'; END IF;
 IF e.id IS NULL OR e.runner_id<>r.id OR NOT e.reservation_held OR e.body->>'state'<>'dispatched' OR r.status<>'active' OR r.token_hash='' OR NOT('database_backup_retention'=ANY(r.capabilities)) OR NOT((CASE WHEN e.plan->'operation'->>'version'='mysql.backup-retention.v1' THEN 'hosting_mysql_backup_retention_v1' ELSE 'hosting_postgresql_backup_retention_v1' END)=ANY(r.capabilities)) OR 'shell'=ANY(r.capabilities) OR spec<>jsonb_build_object('type','database_backup_retention','hosting_operation_id',e.id,'hosting_mode','retention','inputs','{}'::jsonb) OR (EXISTS(SELECT 1 FROM run_leases WHERE run_id=NEW.run_id) AND (NOT EXISTS(SELECT 1 FROM hosting_database_backup_retention_resumes a WHERE a.execution_id=e.id AND a.next_attempt=NEW.attempt AND a.plan_hash=e.plan_hash) OR EXISTS(SELECT 1 FROM run_leases prior_lease WHERE prior_lease.run_id=NEW.run_id AND prior_lease.status='active' AND prior_lease.expires_at>clock_timestamp()))) THEN RAISE EXCEPTION 'retention lease identity mismatch' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;

CREATE FUNCTION nerocd_mysql_component_retention_schema_compatible() RETURNS boolean LANGUAGE sql STABLE AS $$ SELECT true $$;

DO $migration$
DECLARE owner_schema text:=current_schema(); routine text;
BEGIN
 FOREACH routine IN ARRAY ARRAY['nerocd_backup_retention_source_scope()','nerocd_backup_retention_lease()','nerocd_mysql_component_retention_schema_compatible()'] LOOP
  EXECUTE format('ALTER FUNCTION %I.%s SET search_path TO pg_catalog, %I, pg_temp',owner_schema,routine,owner_schema);
 END LOOP;
END;$migration$;
