-- Legacy repositories have an empty policy and are deliberately
-- non-deployable. Maintainers must explicitly configure an admitted source.
ALTER TABLE repositories ADD COLUMN repository_policy JSONB NOT NULL DEFAULT '{"version":1,"state":"legacy_unverified"}'::jsonb;
ALTER TABLE repositories ADD CONSTRAINT repositories_policy_object CHECK (jsonb_typeof(repository_policy)='object');
ALTER TABLE repositories ADD CONSTRAINT repositories_policy_state CHECK ((repository_policy->>'state') IN ('configured','legacy_unverified') AND (repository_policy->>'version')='1');
CREATE OR REPLACE FUNCTION nerocd_repository_policy_schema_compatible() RETURNS boolean LANGUAGE sql STABLE AS $function$
 SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='repositories' AND column_name='repository_policy' AND is_nullable='NO');
$function$;
