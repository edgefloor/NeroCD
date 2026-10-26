-- Explicit manual selection shares immutable copy reservations and receipts.
ALTER TABLE hosting_storage_backup_retention_requests DROP CONSTRAINT hosting_storage_backup_retention_requests_kind_check;
ALTER TABLE hosting_storage_backup_retention_requests ADD CONSTRAINT hosting_storage_backup_retention_requests_kind_check CHECK(kind IN ('policy','preview','execution','deletion_preview','deletion_execution'));
CREATE FUNCTION nerocd_storage_deletion_lease() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE plan jsonb;
BEGIN
 SELECT e.plan INTO plan FROM hosting_storage_backup_retention_executions e WHERE e.run_id=NEW.run_id;
 IF plan->'operation'->>'origin'='manual_selection' AND NOT EXISTS(SELECT 1 FROM runners r WHERE r.id=NEW.runner_id AND 'hosting_storage_backup_deletion_v1'=ANY(r.capabilities)) THEN
 RAISE EXCEPTION 'manual deletion requires compatible runner' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER hosting_storage_backup_deletion_lease BEFORE INSERT ON run_leases FOR EACH ROW EXECUTE FUNCTION nerocd_storage_deletion_lease();
DO $$ DECLARE owner_schema text:=current_schema(); BEGIN
 EXECUTE format('ALTER FUNCTION %I.nerocd_storage_deletion_lease() SET search_path=%I,pg_catalog,pg_temp',owner_schema,owner_schema);
END $$;
