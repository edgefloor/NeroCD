-- Native MySQL remote restore reuses exact immutable upload and restore identities.
CREATE OR REPLACE FUNCTION nerocd_backup_remote_admission() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE snapshot jsonb; original hosting_database_backups; parent hosting_database_backup_transfers; restore hosting_database_backups; transfer_version text; object_key text;
BEGIN
 SELECT v.snapshot INTO snapshot FROM hosting_database_backup_target_versions v JOIN hosting_database_backup_targets t ON t.id=v.target_id WHERE v.target_id=NEW.target_id AND v.revision=NEW.target_revision AND t.project_id=NEW.project_id;
 IF snapshot IS NULL OR NEW.plan->'target' IS DISTINCT FROM snapshot OR NEW.plan->>'hash' IS DISTINCT FROM NEW.plan_hash OR NEW.plan->>'actor_id' IS DISTINCT FROM NEW.actor_id OR NEW.plan->>'runner_id' IS DISTINCT FROM NEW.runner_id OR NEW.plan->'operation'->>'id' IS DISTINCT FROM NEW.id OR NEW.plan->'operation'->>'project_id' IS DISTINCT FROM NEW.project_id OR NEW.plan->'operation'->>'target_id' IS DISTINCT FROM NEW.target_id OR NEW.plan->'operation'->>'target_revision' IS DISTINCT FROM NEW.target_revision::text OR NEW.plan->'operation'->>'destination_id' IS DISTINCT FROM NEW.destination_id OR NEW.plan->'operation'->>'mode' IS DISTINCT FROM NEW.mode OR NEW.state<>'queued' OR NEW.run_id IS NOT NULL OR NEW.failure_code<>'' THEN
  RAISE EXCEPTION 'remote backup admission identity mismatch' USING ERRCODE='23514';
 END IF;
 IF NEW.mode='validate' AND NEW.plan->'operation'->>'version' IS DISTINCT FROM 'postgresql.logical-s3-transfer.v1' THEN RAISE EXCEPTION 'remote target validation version mismatch' USING ERRCODE='23514'; END IF;
 IF NEW.mode<>'validate' THEN
  SELECT * INTO original FROM hosting_database_backups WHERE id=NEW.backup_id;
  transfer_version:='postgresql.logical-s3-transfer.v1';
  object_key:='nerocd/postgresql/'||NEW.backup_id||'/'||substring(NEW.blob->>'digest' from 8)||'.dump';
  IF original.plan->'operation'->>'version'='mysql.logical-local.v1' THEN
   transfer_version:='mysql.logical-s3-transfer.v1';
   object_key:='nerocd/mysql/'||NEW.backup_id||'/'||substring(NEW.blob->>'digest' from 8)||'.sql';
   IF NEW.mode NOT IN ('upload','download') OR NEW.blob->>'format' IS DISTINCT FROM 'mysql_sql' OR original.plan->'source'->'snapshot'->'database'->>'engine' IS DISTINCT FROM 'mysql' OR original.plan->'source'->'snapshot'->'database'->>'recipe_version' IS DISTINCT FROM 'mysql-8.4.13-oraclelinux9.v1' THEN RAISE EXCEPTION 'MySQL remote transfer engine mismatch' USING ERRCODE='23514'; END IF;
  ELSIF NEW.blob->>'format' IS DISTINCT FROM 'postgresql_custom' THEN RAISE EXCEPTION 'PostgreSQL remote archive format mismatch' USING ERRCODE='23514'; END IF;
  IF NEW.plan->'operation'->>'version' IS DISTINCT FROM transfer_version THEN RAISE EXCEPTION 'remote backup transfer version mismatch' USING ERRCODE='23514'; END IF;
  IF original.id IS NULL OR original.mode<>'backup' OR original.state<>'succeeded' OR original.project_id<>NEW.project_id OR original.blob IS DISTINCT FROM NEW.blob OR NEW.plan->'operation'->'blob' IS DISTINCT FROM NEW.blob OR NEW.plan->'operation'->>'backup_id' IS DISTINCT FROM NEW.backup_id OR original.plan->'source'->'snapshot'->>'destination_id' IS DISTINCT FROM NEW.destination_id OR original.runner_id<>NEW.runner_id OR original.plan->'source'->'release'->>'daemon_id' IS DISTINCT FROM NEW.daemon_id OR NEW.plan->>'daemon_id' IS DISTINCT FROM NEW.daemon_id OR NEW.plan->>'location_id' IS DISTINCT FROM NEW.backup_id OR NEW.plan->>'object_key' IS DISTINCT FROM object_key THEN
   RAISE EXCEPTION 'remote backup blob identity mismatch' USING ERRCODE='23514';
  END IF;
 END IF;
 IF NEW.predecessor_id IS NOT NULL THEN
  SELECT * INTO parent FROM hosting_database_backup_transfers WHERE id=NEW.predecessor_id;
  IF parent.id IS NULL OR parent.mode<>'upload' OR parent.backup_id IS DISTINCT FROM NEW.backup_id OR parent.blob IS DISTINCT FROM NEW.blob OR parent.target_id<>NEW.target_id OR parent.target_revision<>NEW.target_revision OR parent.destination_id<>NEW.destination_id OR parent.runner_id<>NEW.runner_id OR parent.plan->>'object_key' IS DISTINCT FROM NEW.plan->>'object_key' OR parent.plan->'target' IS DISTINCT FROM NEW.plan->'target' OR (NEW.mode='upload' AND parent.state NOT IN ('failed','uncertain')) OR (NEW.mode='download' AND parent.state<>'succeeded') THEN
   RAISE EXCEPTION 'remote backup predecessor mismatch' USING ERRCODE='23514';
  END IF;
 END IF;
 IF NEW.mode='download' THEN
  SELECT * INTO restore FROM hosting_database_backups WHERE id=NEW.restore_operation_id;
  IF NEW.predecessor_id IS NULL OR restore.id IS NULL OR restore.mode<>'restore' OR NOT restore.ownership_held OR restore.state<>'waiting_target' OR restore.backup_id<>NEW.backup_id OR restore.project_id<>NEW.project_id OR NEW.plan->'operation'->>'restore_operation_id' IS DISTINCT FROM restore.id THEN
   RAISE EXCEPTION 'remote backup download restore mismatch' USING ERRCODE='23514';
  END IF;
 END IF;
 RETURN NEW;
END $$;

CREATE OR REPLACE FUNCTION nerocd_backup_remote_lease() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE b hosting_database_backup_transfers; r runners; spec jsonb;
BEGIN
 SELECT run_spec INTO spec FROM task_runs WHERE id=NEW.run_id;
 IF spec->>'type' IS DISTINCT FROM 'database_backup_transfer' THEN RETURN NEW; END IF;
 SELECT * INTO b FROM hosting_database_backup_transfers WHERE run_id=NEW.run_id;
 SELECT * INTO r FROM runners WHERE id=NEW.runner_id;
 IF b.id IS NULL OR b.runner_id IS DISTINCT FROM r.id OR b.state<>'dispatched' OR r.status<>'active' OR r.token_hash='' OR NOT('database_backup_transfer'=ANY(r.capabilities)) OR NOT((CASE WHEN b.plan->'operation'->>'version'='mysql.logical-s3-transfer.v1' AND b.mode='download' THEN 'hosting_mysql_remote_restore_v1' WHEN b.plan->'operation'->>'version'='mysql.logical-s3-transfer.v1' THEN 'hosting_mysql_remote_backup_v1' ELSE 'hosting_postgresql_remote_backup_v1' END)=ANY(r.capabilities)) OR 'shell'=ANY(r.capabilities) OR spec<>jsonb_build_object('type','database_backup_transfer','inputs','{}'::jsonb,'hosting_operation_id',b.id,'hosting_mode',b.mode) OR EXISTS(SELECT 1 FROM run_leases WHERE run_id=NEW.run_id) OR NOT EXISTS(SELECT 1 FROM runner_enrollments e WHERE e.runner_id=r.id AND e.used_at IS NOT NULL AND e.revoked_at IS NULL AND e.credential_hash=r.token_hash) THEN
  RAISE EXCEPTION 'remote backup lease identity mismatch' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;

CREATE FUNCTION nerocd_mysql_remote_restore_schema_compatible() RETURNS boolean LANGUAGE sql STABLE AS $$ SELECT true $$;

DO $migration$
DECLARE owner_schema text:=current_schema(); routine text;
BEGIN
 FOREACH routine IN ARRAY ARRAY['nerocd_backup_remote_admission()','nerocd_backup_remote_lease()','nerocd_mysql_remote_restore_schema_compatible()'] LOOP
  EXECUTE format('ALTER FUNCTION %I.%s SET search_path TO pg_catalog, %I, pg_temp',owner_schema,routine,owner_schema);
 END LOOP;
END;$migration$;
