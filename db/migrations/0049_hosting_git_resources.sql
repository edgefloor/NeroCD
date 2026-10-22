-- Resource Git references and immutable source versions commit as one command.
-- Historical image sidecars remain readable, but cannot become new Git builds.
ALTER TABLE resources DROP CONSTRAINT resources_source_check;
ALTER TABLE resources ADD CONSTRAINT resources_source_check CHECK ((
 (source->>'kind'='image' AND jsonb_typeof(source->'image')='object' AND jsonb_typeof(source->'image'->'reference')='string' AND length(source->'image'->>'reference') BETWEEN 1 AND 512 AND NOT source ? 'git')
 OR
 (source->>'kind'='git' AND source - 'kind' - 'git'='{}'::jsonb AND jsonb_typeof(source->'git')='object' AND (source->'git') - 'source_id' - 'generation'='{}'::jsonb AND jsonb_typeof(source->'git'->'source_id')='string' AND length(source->'git'->>'source_id') BETWEEN 1 AND 128 AND jsonb_typeof(source->'git'->'generation')='number' AND (source->'git'->>'generation')::bigint>0)
) IS TRUE);

CREATE FUNCTION nerocd_hosting_git_reference_consistent() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE resource_key text; desired jsonb; resource_project text;
BEGIN
 IF TG_TABLE_NAME='resources' THEN resource_key:=NEW.id;
 ELSIF TG_TABLE_NAME='hosting_build_sources' THEN resource_key:=NEW.resource_id;
 ELSE SELECT resource_id INTO resource_key FROM hosting_build_sources WHERE id=NEW.source_id;
 END IF;
 SELECT source,project_id INTO desired,resource_project FROM resources WHERE id=resource_key;
 IF desired->>'kind'='git' THEN
  IF NOT EXISTS(SELECT 1 FROM hosting_build_sources h JOIN hosting_build_source_versions v ON v.source_id=h.id AND v.generation=h.generation
   WHERE h.resource_id=resource_key AND h.project_id=resource_project AND h.id=desired->'git'->>'source_id' AND h.generation=(desired->'git'->>'generation')::bigint
   AND v.body->>'resource_id'=resource_key AND v.body->>'project_id'=resource_project) THEN
   RAISE EXCEPTION 'Git resource reference must match current source version' USING ERRCODE='23514';
  END IF;
 ELSIF TG_TABLE_NAME<>'resources' THEN
  RAISE EXCEPTION 'new source versions require a Git resource' USING ERRCODE='23514';
 END IF;
 RETURN NULL;
END;$$;
CREATE CONSTRAINT TRIGGER resources_git_reference AFTER INSERT OR UPDATE ON resources DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_git_reference_consistent();
CREATE CONSTRAINT TRIGGER hosting_sources_resource_reference AFTER INSERT OR UPDATE ON hosting_build_sources DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_git_reference_consistent();
CREATE CONSTRAINT TRIGGER hosting_source_versions_resource_reference AFTER INSERT ON hosting_build_source_versions DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_git_reference_consistent();
CREATE FUNCTION nerocd_hosting_source_kind_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.source->>'kind' IS DISTINCT FROM OLD.source->>'kind' THEN RAISE EXCEPTION 'resource source conversion is unsupported' USING ERRCODE='23514'; END IF;
 IF NEW.source->>'kind'='git' AND NEW.source->'git'->>'source_id' IS DISTINCT FROM OLD.source->'git'->>'source_id' THEN RAISE EXCEPTION 'Git source identity is immutable' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END;$$;
CREATE TRIGGER resources_source_kind BEFORE UPDATE ON resources FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_source_kind_immutable();
CREATE FUNCTION nerocd_hosting_git_resource_schema_compatible() RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT nerocd_hosting_build_schema_compatible()
 AND EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid=to_regclass(current_schema()||'.resources') AND conname='resources_source_check' AND convalidated)
 AND EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass(current_schema()||'.resources') AND tgname='resources_git_reference' AND tgdeferrable AND tginitdeferred AND tgenabled IN('O','A'))
 AND EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass(current_schema()||'.resources') AND tgname='resources_source_kind' AND tgenabled IN('O','A'))
 AND EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass(current_schema()||'.hosting_build_sources') AND tgname='hosting_sources_resource_reference' AND tgdeferrable AND tginitdeferred AND tgenabled IN('O','A'))
 AND EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass(current_schema()||'.hosting_build_source_versions') AND tgname='hosting_source_versions_resource_reference' AND tgdeferrable AND tginitdeferred AND tgenabled IN('O','A'));
$$;
DO $migration$
DECLARE owner_schema text:=current_schema();
BEGIN
 EXECUTE format('ALTER FUNCTION %I.nerocd_hosting_git_reference_consistent() SET search_path TO pg_catalog, %I, pg_temp',owner_schema,owner_schema);
 EXECUTE format('ALTER FUNCTION %I.nerocd_hosting_source_kind_immutable() SET search_path TO pg_catalog, %I, pg_temp',owner_schema,owner_schema);
 EXECUTE format('ALTER FUNCTION %I.nerocd_hosting_git_resource_schema_compatible() SET search_path TO %I, pg_catalog, pg_temp',owner_schema,owner_schema);
END;$migration$;
