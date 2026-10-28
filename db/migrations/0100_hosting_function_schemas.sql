-- Hosting triggers must resolve their tables and row types in the
-- owning schema even when invoked by a caller with a different search path.
DO $migration$
DECLARE owner_schema text := current_schema(); routine text;
BEGIN
 FOREACH routine IN ARRAY ARRAY[
  'nerocd_hosting_compose_lease()',
  'nerocd_component_backup_lease()',
  'nerocd_component_backup_stack_hold()',
  'nerocd_database_backup_admission()',
  'nerocd_database_backup_receipt()'
 ] LOOP
  EXECUTE format('ALTER FUNCTION %I.%s SET search_path TO pg_catalog, %I, pg_temp',owner_schema,routine,owner_schema);
 END LOOP;
END;$migration$;
