-- Ephemeral download metadata only. Archive bytes remain in the runner CAS.
CREATE TABLE hosting_storage_backup_downloads (
 id text PRIMARY KEY,
 backup_id text NOT NULL REFERENCES hosting_storage_backups(id) ON DELETE RESTRICT,
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
CREATE INDEX hosting_storage_backup_download_active ON hosting_storage_backup_downloads(runner_id,expires_at) WHERE state IN ('waiting','streaming');
CREATE FUNCTION nerocd_storage_download_identity() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' OR NEW.id<>OLD.id OR NEW.backup_id<>OLD.backup_id OR NEW.actor_id<>OLD.actor_id OR NEW.runner_id<>OLD.runner_id OR NEW.run_id<>OLD.run_id OR NEW.plan<>OLD.plan OR NEW.plan_hash<>OLD.plan_hash OR NEW.expires_at<>OLD.expires_at OR NEW.created_at<>OLD.created_at OR (OLD.authority IS NOT NULL AND NEW.authority IS DISTINCT FROM OLD.authority) OR OLD.state IN ('succeeded','failed') AND NEW.state<>OLD.state THEN RAISE EXCEPTION 'immutable download identity' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER hosting_storage_download_identity BEFORE UPDATE OR DELETE ON hosting_storage_backup_downloads FOR EACH ROW EXECUTE FUNCTION nerocd_storage_download_identity();
