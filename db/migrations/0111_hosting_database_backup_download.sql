-- Ephemeral download metadata only. Archive bytes remain in the runner CAS.
CREATE TABLE hosting_database_backup_downloads (
 id text PRIMARY KEY,
 backup_id text NOT NULL REFERENCES hosting_database_backups(id) ON DELETE RESTRICT,
 actor_id text NOT NULL REFERENCES principals(id) ON DELETE RESTRICT,
 runner_id text NOT NULL REFERENCES runners(id) ON DELETE RESTRICT,
 run_id text NOT NULL UNIQUE REFERENCES task_runs(id) ON DELETE RESTRICT,
 plan jsonb NOT NULL CHECK(jsonb_typeof(plan)='object'),
 plan_hash text NOT NULL CHECK(plan_hash ~ '^sha256:[a-f0-9]{64}$'),
 expires_at timestamptz NOT NULL,
 state text NOT NULL DEFAULT 'waiting' CHECK(state IN ('waiting','streaming','succeeded','failed')),
 authority jsonb,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX hosting_database_backup_download_active ON hosting_database_backup_downloads(runner_id,expires_at) WHERE state IN ('waiting','streaming');
CREATE FUNCTION nerocd_database_download_identity() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' OR NEW.id<>OLD.id OR NEW.backup_id<>OLD.backup_id OR NEW.actor_id<>OLD.actor_id OR NEW.runner_id<>OLD.runner_id OR NEW.run_id<>OLD.run_id OR NEW.plan<>OLD.plan OR NEW.plan_hash<>OLD.plan_hash OR NEW.expires_at<>OLD.expires_at OR NEW.created_at<>OLD.created_at OR (OLD.authority IS NOT NULL AND NEW.authority IS DISTINCT FROM OLD.authority) OR OLD.state IN ('succeeded','failed') AND NEW.state<>OLD.state THEN RAISE EXCEPTION 'immutable download identity' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER hosting_database_download_identity BEFORE UPDATE OR DELETE ON hosting_database_backup_downloads FOR EACH ROW EXECUTE FUNCTION nerocd_database_download_identity();

-- Preserve remote restore/upload admission; this local read has its own receipt.
CREATE OR REPLACE FUNCTION nerocd_backup_remote_lease() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE b hosting_database_backup_transfers; d hosting_database_backup_downloads; r runners; spec jsonb;
BEGIN
 SELECT run_spec INTO spec FROM task_runs WHERE id=NEW.run_id;
 IF spec->>'type' IS DISTINCT FROM 'database_backup_transfer' THEN RETURN NEW; END IF;
 IF spec->>'hosting_mode'='browser_download' THEN
  SELECT * INTO d FROM hosting_database_backup_downloads WHERE run_id=NEW.run_id;
  SELECT * INTO r FROM runners WHERE id=NEW.runner_id;
  IF d.id IS NULL OR d.runner_id IS DISTINCT FROM r.id OR d.state<>'waiting' OR d.expires_at<=clock_timestamp() OR r.status<>'active' OR r.token_hash='' OR NOT('database_backup_transfer'=ANY(r.capabilities)) OR NOT('hosting_database_backup_download_v1'=ANY(r.capabilities)) OR 'shell'=ANY(r.capabilities) OR NEW.run_id<>'db_transfer_'||d.id OR spec<>jsonb_build_object('type','database_backup_transfer','inputs','{}'::jsonb,'hosting_operation_id',d.id,'hosting_mode','browser_download') OR d.plan->>'version' IS DISTINCT FROM 'database.logical-local-download.v1' OR d.plan->>'session_id' IS DISTINCT FROM d.id OR d.plan->>'runner_id' IS DISTINCT FROM r.id OR d.plan->>'hash' IS DISTINCT FROM d.plan_hash OR EXISTS(SELECT 1 FROM run_leases WHERE run_id=NEW.run_id) OR NOT EXISTS(SELECT 1 FROM runner_enrollments e WHERE e.runner_id=r.id AND e.used_at IS NOT NULL AND e.revoked_at IS NULL AND e.credential_hash=r.token_hash) THEN
   RAISE EXCEPTION 'database download lease identity mismatch' USING ERRCODE='23514';
  END IF;
  RETURN NEW;
 END IF;
 SELECT * INTO b FROM hosting_database_backup_transfers WHERE run_id=NEW.run_id;
 SELECT * INTO r FROM runners WHERE id=NEW.runner_id;
 IF b.id IS NULL OR b.runner_id IS DISTINCT FROM r.id OR b.state<>'dispatched' OR r.status<>'active' OR r.token_hash='' OR NOT('database_backup_transfer'=ANY(r.capabilities)) OR NOT((CASE WHEN b.plan->'operation'->>'version'='mysql.logical-s3-transfer.v1' AND b.mode='download' THEN 'hosting_mysql_remote_restore_v1' WHEN b.plan->'operation'->>'version'='mysql.logical-s3-transfer.v1' THEN 'hosting_mysql_remote_backup_v1' ELSE 'hosting_postgresql_remote_backup_v1' END)=ANY(r.capabilities)) OR 'shell'=ANY(r.capabilities) OR spec<>jsonb_build_object('type','database_backup_transfer','inputs','{}'::jsonb,'hosting_operation_id',b.id,'hosting_mode',b.mode) OR EXISTS(SELECT 1 FROM run_leases WHERE run_id=NEW.run_id) OR NOT EXISTS(SELECT 1 FROM runner_enrollments e WHERE e.runner_id=r.id AND e.used_at IS NOT NULL AND e.revoked_at IS NULL AND e.credential_hash=r.token_hash) THEN
  RAISE EXCEPTION 'remote backup lease identity mismatch' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;

DO $migration$
DECLARE owner_schema text:=current_schema(); routine text;
BEGIN
 FOREACH routine IN ARRAY ARRAY['nerocd_database_download_identity()','nerocd_backup_remote_lease()'] LOOP
  EXECUTE format('ALTER FUNCTION %I.%s SET search_path TO pg_catalog, %I, pg_temp',owner_schema,routine,owner_schema);
 END LOOP;
END;$migration$;
