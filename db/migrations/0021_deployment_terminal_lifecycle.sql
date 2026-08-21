ALTER TABLE deployment_attempts
    ADD COLUMN status TEXT NOT NULL DEFAULT 'active'
        CHECK (status IN ('active','succeeded','failed','canceled'));

CREATE OR REPLACE FUNCTION nerocd_deployment_schema_compatible() RETURNS boolean LANGUAGE sql STABLE AS $function$
 SELECT to_regclass(current_schema() || '.deployments') IS NOT NULL
    AND to_regclass(current_schema() || '.services') IS NOT NULL
    AND to_regclass(current_schema() || '.deployment_attempts') IS NOT NULL
    AND to_regclass(current_schema() || '.deployment_transitions') IS NOT NULL
    AND EXISTS (
      SELECT 1 FROM information_schema.columns
      WHERE table_schema=current_schema() AND table_name='deployment_attempts'
        AND column_name='status' AND is_nullable='NO'
    );
$function$;
