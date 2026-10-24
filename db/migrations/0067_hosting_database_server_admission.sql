-- Database-only runners use the existing enrolled server/destination identity
-- without claiming support for Compose workloads.
CREATE OR REPLACE FUNCTION nerocd_server_runner_admission() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE candidate runners%ROWTYPE;
BEGIN
 SELECT * INTO candidate FROM runners WHERE id=NEW.runner_id FOR SHARE;
 IF NOT FOUND OR candidate.status<>'active' OR candidate.token_hash='' OR NOT (
  candidate.capabilities && ARRAY['compose-deploy','compose_deploy']::text[]
  OR candidate.capabilities @> ARRAY['database_deploy','hosting_database_v1','hosting_owned_storage_v1']::text[]
 ) OR 'shell'=ANY(candidate.capabilities) THEN
 RAISE EXCEPTION 'server runner is not eligible' USING ERRCODE='23514'; END IF;
 PERFORM 1 FROM runner_enrollments WHERE runner_id=NEW.runner_id AND used_at IS NOT NULL AND revoked_at IS NULL FOR SHARE;
 IF NOT FOUND THEN RAISE EXCEPTION 'server runner is not enrolled' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END;
$$;

-- Require this upgrade and detect restoration of the Compose-only admission
-- function even if the additive readiness marker remains installed.
CREATE OR REPLACE FUNCTION nerocd_hosting_database_server_schema_compatible() RETURNS boolean LANGUAGE sql STABLE AS $$
SELECT EXISTS (
 SELECT 1 FROM pg_proc
 WHERE oid=to_regprocedure(current_schema() || '.nerocd_server_runner_admission()')
 AND position('candidate.capabilities && ARRAY[''compose-deploy'',''compose_deploy'']::text[]' in prosrc)>0
 AND position('candidate.capabilities @> ARRAY[''database_deploy'',''hosting_database_v1'',''hosting_owned_storage_v1'']::text[]' in prosrc)>0
);
$$;
