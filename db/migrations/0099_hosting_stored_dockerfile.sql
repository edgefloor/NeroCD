-- Stored Dockerfiles reuse immutable source versions without Git transport identity.
ALTER TABLE resources DROP CONSTRAINT resources_source_check;
ALTER TABLE resources ADD CONSTRAINT resources_source_check CHECK ((
 (source->>'kind'='image' AND jsonb_typeof(source->'image')='object' AND jsonb_typeof(source->'image'->'reference')='string' AND length(source->'image'->>'reference') BETWEEN 1 AND 512 AND NOT source ? 'git' AND NOT source ? 'compose' AND NOT source ? 'dockerfile')
 OR
 (source->>'kind'='git' AND source - 'kind' - 'git'='{}'::jsonb AND jsonb_typeof(source->'git')='object' AND (source->'git') - 'source_id' - 'generation'='{}'::jsonb AND jsonb_typeof(source->'git'->'source_id')='string' AND length(source->'git'->>'source_id') BETWEEN 1 AND 128 AND jsonb_typeof(source->'git'->'generation')='number' AND (source->'git'->>'generation')::bigint>0)
 OR
 (source->>'kind'='compose' AND source - 'kind' - 'compose'='{}'::jsonb AND jsonb_typeof(source->'compose')='object' AND (source->'compose') - 'original' - 'public_component' - 'container_port'='{}'::jsonb
  AND jsonb_typeof(source->'compose'->'original')='string' AND octet_length(source->'compose'->>'original') BETWEEN 1 AND 131072
  AND jsonb_typeof(source->'compose'->'public_component')='string' AND source->'compose'->>'public_component' ~ '^[a-z][a-z0-9_-]{0,62}$'
  AND jsonb_typeof(source->'compose'->'container_port')='number' AND (source->'compose'->>'container_port')::numeric BETWEEN 1 AND 65535
  AND (source->'compose'->>'container_port')::numeric=trunc((source->'compose'->>'container_port')::numeric))
 OR
 (source->>'kind'='dockerfile' AND source - 'kind' - 'dockerfile'='{}'::jsonb AND jsonb_typeof(source->'dockerfile')='object'
  AND (source->'dockerfile') - 'source_id' - 'generation'='{}'::jsonb
  AND jsonb_typeof(source->'dockerfile'->'source_id')='string' AND length(source->'dockerfile'->>'source_id') BETWEEN 1 AND 128
  AND jsonb_typeof(source->'dockerfile'->'generation')='number' AND (source->'dockerfile'->>'generation')::bigint>0)
) IS TRUE);

CREATE OR REPLACE FUNCTION nerocd_hosting_git_reference_consistent() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE resource_key text; desired jsonb; resource_project text; source_kind text;
BEGIN
 IF TG_TABLE_NAME='resources' THEN resource_key:=NEW.id;
 ELSIF TG_TABLE_NAME='hosting_build_sources' THEN resource_key:=NEW.resource_id;
 ELSE SELECT resource_id INTO resource_key FROM hosting_build_sources WHERE id=NEW.source_id;
 END IF;
 SELECT source,project_id INTO desired,resource_project FROM resources WHERE id=resource_key;
 source_kind:=desired->>'kind';
 IF source_kind IN('git','dockerfile') THEN
  IF NOT EXISTS(SELECT 1 FROM hosting_build_sources h JOIN hosting_build_source_versions v ON v.source_id=h.id AND v.generation=h.generation
   WHERE h.resource_id=resource_key AND h.project_id=resource_project AND h.id=desired->source_kind->>'source_id' AND h.generation=(desired->source_kind->>'generation')::bigint
   AND coalesce(v.body->>'kind','git')=source_kind AND v.body->>'resource_id'=resource_key AND v.body->>'project_id'=resource_project) THEN
   RAISE EXCEPTION 'Git resource reference must match current source version' USING ERRCODE='23514';
  END IF;
 ELSIF TG_TABLE_NAME<>'resources' THEN
  RAISE EXCEPTION 'new source versions require a Git resource' USING ERRCODE='23514';
 END IF;
 RETURN NULL;
END;$$;
CREATE OR REPLACE FUNCTION nerocd_hosting_source_kind_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.source->>'kind' IS DISTINCT FROM OLD.source->>'kind' THEN RAISE EXCEPTION 'resource source conversion is unsupported' USING ERRCODE='23514'; END IF;
 IF NEW.source->>'kind' IN('git','dockerfile') AND NEW.source->(NEW.source->>'kind')->>'source_id' IS DISTINCT FROM OLD.source->(OLD.source->>'kind')->>'source_id' THEN RAISE EXCEPTION 'Git source identity is immutable' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END;$$;
-- Raise only the stored-source document bounds; retain existing Git limits.
ALTER TABLE hosting_build_source_versions DROP CONSTRAINT hosting_build_source_versions_body_check;
ALTER TABLE hosting_build_source_versions ADD CONSTRAINT hosting_build_source_versions_body_check CHECK(jsonb_typeof(body)='object' AND octet_length(body::text)<=CASE WHEN body->>'kind'='dockerfile' THEN 8388608 ELSE 65536 END);
ALTER TABLE hosting_builds DROP CONSTRAINT hosting_builds_snapshot_check;
ALTER TABLE hosting_builds ADD CONSTRAINT hosting_builds_snapshot_check CHECK(jsonb_typeof(snapshot)='object' AND octet_length(snapshot::text)<=CASE WHEN snapshot->'source'->>'kind'='dockerfile' THEN 8388608 ELSE 131072 END);
DO $migration$
DECLARE owner_schema text:=current_schema();
BEGIN
 EXECUTE format('ALTER FUNCTION %I.nerocd_hosting_git_reference_consistent() SET search_path TO pg_catalog, %I, pg_temp',owner_schema,owner_schema);
 EXECUTE format('ALTER FUNCTION %I.nerocd_hosting_source_kind_immutable() SET search_path TO pg_catalog, %I, pg_temp',owner_schema,owner_schema);
END;$migration$;
