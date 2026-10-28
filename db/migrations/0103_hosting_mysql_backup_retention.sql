CREATE OR REPLACE FUNCTION nerocd_backup_retention_lease() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE e hosting_database_backup_retention_executions; r runners; spec jsonb;
BEGIN
 SELECT run_spec INTO spec FROM task_runs WHERE id=NEW.run_id;
 IF spec->>'type' IS DISTINCT FROM 'database_backup_retention' THEN RETURN NEW; END IF;
 SELECT * INTO e FROM hosting_database_backup_retention_executions WHERE run_id=NEW.run_id;
 SELECT * INTO r FROM runners WHERE id=NEW.runner_id;
 IF (e.plan->'operation'->>'version' IS DISTINCT FROM 'postgresql.backup-retention.v1' AND e.plan->'operation'->>'version' IS DISTINCT FROM 'mysql.backup-retention.v1') OR (e.plan->'operation'->>'version'='mysql.backup-retention.v1' AND NOT EXISTS(SELECT 1 FROM hosting_databases d WHERE d.resource_id=e.resource_id AND d.engine='mysql' AND d.recipe_version='mysql-8.4.13-oraclelinux9.v1')) THEN RAISE EXCEPTION 'retention engine identity mismatch' USING ERRCODE='23514'; END IF;
 IF e.id IS NULL OR e.runner_id<>r.id OR NOT e.reservation_held OR e.body->>'state'<>'dispatched' OR r.status<>'active' OR r.token_hash='' OR NOT('database_backup_retention'=ANY(r.capabilities)) OR NOT((CASE WHEN e.plan->'operation'->>'version'='mysql.backup-retention.v1' THEN 'hosting_mysql_backup_retention_v1' ELSE 'hosting_postgresql_backup_retention_v1' END)=ANY(r.capabilities)) OR 'shell'=ANY(r.capabilities) OR spec<>jsonb_build_object('type','database_backup_retention','hosting_operation_id',e.id,'hosting_mode','retention','inputs','{}'::jsonb) OR (EXISTS(SELECT 1 FROM run_leases WHERE run_id=NEW.run_id) AND (NOT EXISTS(SELECT 1 FROM hosting_database_backup_retention_resumes a WHERE a.execution_id=e.id AND a.next_attempt=NEW.attempt AND a.plan_hash=e.plan_hash) OR EXISTS(SELECT 1 FROM run_leases prior_lease WHERE prior_lease.run_id=NEW.run_id AND prior_lease.status='active' AND prior_lease.expires_at>clock_timestamp()))) THEN RAISE EXCEPTION 'retention lease identity mismatch' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE OR REPLACE FUNCTION nerocd_backup_retention_receipt() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE e hosting_database_backup_retention_executions; l run_leases; i hosting_database_backup_retention_items; probe_prefix text;
BEGIN
 SELECT * INTO e FROM hosting_database_backup_retention_executions WHERE id=NEW.execution_id;
 SELECT * INTO l FROM run_leases WHERE id=NEW.authority->>'lease_id';
 SELECT * INTO i FROM hosting_database_backup_retention_items WHERE execution_id=NEW.execution_id AND copy_id=NEW.copy_id;
 IF e.id IS NULL OR l.id IS NULL OR i.copy_id IS NULL OR e.run_id IS DISTINCT FROM l.run_id OR e.runner_id IS DISTINCT FROM l.runner_id OR NEW.authority->>'operation_id' IS DISTINCT FROM e.id OR NEW.authority->>'run_id' IS DISTINCT FROM e.run_id OR NEW.authority->>'attempt' IS DISTINCT FROM l.attempt::text OR NEW.authority->>'fence' IS DISTINCT FROM l.fence OR l.expires_at<=clock_timestamp() OR NEW.response IS DISTINCT FROM e.body OR (NEW.phase NOT LIKE 'probe_%' AND NEW.phase IS DISTINCT FROM i.state) THEN RAISE EXCEPTION 'retention receipt authority mismatch' USING ERRCODE='23514'; END IF;
 IF NEW.attempt<>l.attempt OR NEW.body->>'receipt_id' IS DISTINCT FROM NEW.receipt_id OR NEW.body->>'copy_id' IS DISTINCT FROM NEW.copy_id OR NEW.body->>'plan_hash' IS DISTINCT FROM e.plan_hash OR NEW.body - ARRAY['receipt_id','copy_id','plan_hash','phase','failure_code','probe_only','probe_key'] IS DISTINCT FROM NEW.authority OR NEW.phase IS DISTINCT FROM (CASE WHEN NEW.body->>'probe_only'='true' THEN 'probe_'||(NEW.body->>'phase') ELSE NEW.body->>'phase' END) THEN RAISE EXCEPTION 'retention receipt body mismatch' USING ERRCODE='23514'; END IF;
 IF (e.body->>'state' IN ('dispatched','applying') AND l.status<>'active') OR (e.body->>'state'='succeeded' AND l.status<>'succeeded') OR (e.body->>'state' IN ('failed','uncertain') AND l.status<>'failed') THEN RAISE EXCEPTION 'retention receipt settlement mismatch' USING ERRCODE='23514'; END IF;
 IF NEW.body->>'probe_only'='true' THEN
  probe_prefix:=(CASE WHEN e.plan->'operation'->>'version'='mysql.backup-retention.v1' THEN 'nerocd/mysql/retention-probes/' ELSE 'nerocd/postgresql/retention-probes/' END)||e.id||'/';
  IF left(NEW.body->>'probe_key',length(probe_prefix)) IS DISTINCT FROM probe_prefix OR length(NEW.body->>'probe_key') IS DISTINCT FROM length(probe_prefix)+32 OR right(NEW.body->>'probe_key',32) !~ '^[0-9a-f]{32}$' THEN RAISE EXCEPTION 'retention probe engine identity mismatch' USING ERRCODE='23514'; END IF;
 END IF;
 RETURN NEW;
END $$;

CREATE FUNCTION nerocd_mysql_backup_retention_schema_compatible() RETURNS boolean LANGUAGE sql STABLE AS $$ SELECT true $$;
DO $migration$
DECLARE owner_schema text:=current_schema(); routine text;
BEGIN
 FOREACH routine IN ARRAY ARRAY['nerocd_backup_retention_receipt()','nerocd_backup_retention_lease()','nerocd_mysql_backup_retention_schema_compatible()'] LOOP
  EXECUTE format('ALTER FUNCTION %I.%s SET search_path TO pg_catalog, %I, pg_temp',owner_schema,routine,owner_schema);
 END LOOP;
END;$migration$;
