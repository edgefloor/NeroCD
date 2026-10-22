-- Accept the runner protocol's canonical capability without invalidating
-- destinations enrolled with the historical underscore spelling.
CREATE OR REPLACE FUNCTION nerocd_server_runner_admission() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE candidate runners%ROWTYPE;
BEGIN
 SELECT * INTO candidate FROM runners WHERE id=NEW.runner_id FOR SHARE;
 IF NOT FOUND OR candidate.status<>'active' OR candidate.token_hash='' OR NOT (candidate.capabilities && ARRAY['compose-deploy','compose_deploy']::text[]) OR 'shell'=ANY(candidate.capabilities) THEN
 RAISE EXCEPTION 'server runner is not eligible' USING ERRCODE='23514'; END IF;
 PERFORM 1 FROM runner_enrollments WHERE runner_id=NEW.runner_id AND used_at IS NOT NULL AND revoked_at IS NULL FOR SHARE;
 IF NOT FOUND THEN RAISE EXCEPTION 'server runner is not enrolled' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END;
$$;

-- Readiness must reject both an unupgraded database and restoration of the old
-- admission function while the compatibility marker remains installed.
CREATE FUNCTION nerocd_destination_capability_schema_compatible() RETURNS boolean LANGUAGE sql STABLE AS $$
SELECT EXISTS (
 SELECT 1 FROM pg_proc
 WHERE oid=to_regprocedure(current_schema() || '.nerocd_server_runner_admission()')
 AND position('''compose-deploy''' in prosrc)>0
 AND position('''compose_deploy''' in prosrc)>0
);
$$;
