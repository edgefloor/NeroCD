-- Runtime application roles cannot rewrite audit evidence. The offline schema
-- owner is deliberately privileged enough to alter this trigger only through a
-- reviewed migration; retention is an explicit future archival procedure,
-- never an in-place application rewrite or deletion.
CREATE OR REPLACE FUNCTION nerocd_reject_audit_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  RAISE EXCEPTION 'audit_events is append-only' USING ERRCODE = '55000';
END;
$$;

DROP TRIGGER IF EXISTS audit_events_append_only ON audit_events;
CREATE TRIGGER audit_events_append_only
BEFORE UPDATE OR DELETE ON audit_events
FOR EACH ROW EXECUTE FUNCTION nerocd_reject_audit_mutation();
