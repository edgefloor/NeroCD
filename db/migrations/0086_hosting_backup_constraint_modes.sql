-- sqlc cannot generate SET CONSTRAINTS utility statements. Keep the existing
-- download supersession boundary callable without changing its constraint set.
CREATE FUNCTION nerocd_database_backup_defer_copy_supersession() RETURNS void LANGUAGE plpgsql AS $$
BEGIN
 SET CONSTRAINTS hosting_database_backup_copies_superseded_by_copy_id_fkey, hosting_database_backup_copy_supersession_receipt DEFERRED;
END $$;
DO $$ DECLARE owner_schema text:=current_schema(); BEGIN
 EXECUTE format('ALTER FUNCTION %I.nerocd_database_backup_defer_copy_supersession() SET search_path=%I,pg_catalog,pg_temp',owner_schema,owner_schema);
END $$;
