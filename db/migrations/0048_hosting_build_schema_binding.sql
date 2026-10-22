-- 0047 is already deployed and immutable. Resolve its table/function references
-- against the schema that owns the migration, never the invoking connection's
-- ambient search_path. Explicit pg_temp last prevents temporary-table shadowing.
DO $migration$
DECLARE
 owner_schema text := current_schema();
 function_name text;
BEGIN
 FOREACH function_name IN ARRAY ARRAY[
  'nerocd_hosting_build_source_update',
  'nerocd_hosting_source_grant_update',
  'nerocd_hosting_build_update',
  'nerocd_hosting_build_lease_target',
  'nerocd_hosting_build_source_body',
  'nerocd_hosting_build_artifact_body'
 ] LOOP
  EXECUTE format('ALTER FUNCTION %I.%I() SET search_path TO pg_catalog, %I, pg_temp',owner_schema,function_name,owner_schema);
 END LOOP;
 -- This predicate intentionally uses current_schema() to inspect its own
 -- tables and triggers. Only its trusted owning schema precedes pg_catalog.
 EXECUTE format('ALTER FUNCTION %I.nerocd_hosting_build_schema_compatible() SET search_path TO %I, pg_catalog, pg_temp',owner_schema,owner_schema);
END;
$migration$;
