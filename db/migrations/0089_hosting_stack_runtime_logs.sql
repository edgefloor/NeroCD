ALTER TABLE hosting_runtime_log_events
 DROP CONSTRAINT hosting_runtime_log_events_component_check;
ALTER TABLE hosting_runtime_log_events
 ADD CONSTRAINT hosting_runtime_log_events_component_check
 CHECK(component ~ '^[a-z][a-z0-9_-]{0,62}$');

-- Event labels cannot cross the immutable session component, including legacy
-- single-application sessions whose targets have no component member.
CREATE FUNCTION nerocd_runtime_log_component_identity() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE expected_component text;
BEGIN
 SELECT COALESCE(NULLIF(target->>'component',''),'application')
 INTO expected_component FROM hosting_runtime_log_sessions WHERE id=NEW.session_id;
 IF expected_component IS NULL OR NEW.component IS DISTINCT FROM expected_component THEN
  RAISE EXCEPTION 'runtime log component identity changed' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END;$$;
CREATE TRIGGER hosting_runtime_log_component_identity
 BEFORE INSERT OR UPDATE ON hosting_runtime_log_events
 FOR EACH ROW EXECUTE FUNCTION nerocd_runtime_log_component_identity();
