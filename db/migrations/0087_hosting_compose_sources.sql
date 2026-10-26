-- Managed Compose sources are distinct from image and Git source contracts.
ALTER TABLE resources DROP CONSTRAINT resources_source_check;
ALTER TABLE resources ADD CONSTRAINT resources_source_check CHECK ((
 (source->>'kind'='image' AND jsonb_typeof(source->'image')='object' AND jsonb_typeof(source->'image'->'reference')='string' AND length(source->'image'->>'reference') BETWEEN 1 AND 512 AND NOT source ? 'git' AND NOT source ? 'compose')
 OR
 (source->>'kind'='git' AND source - 'kind' - 'git'='{}'::jsonb AND jsonb_typeof(source->'git')='object' AND (source->'git') - 'source_id' - 'generation'='{}'::jsonb AND jsonb_typeof(source->'git'->'source_id')='string' AND length(source->'git'->>'source_id') BETWEEN 1 AND 128 AND jsonb_typeof(source->'git'->'generation')='number' AND (source->'git'->>'generation')::bigint>0)
 OR
 (source->>'kind'='compose' AND source - 'kind' - 'compose'='{}'::jsonb AND jsonb_typeof(source->'compose')='object' AND (source->'compose') - 'original' - 'public_component' - 'container_port'='{}'::jsonb
  AND jsonb_typeof(source->'compose'->'original')='string' AND octet_length(source->'compose'->>'original') BETWEEN 1 AND 131072
  AND jsonb_typeof(source->'compose'->'public_component')='string' AND source->'compose'->>'public_component' ~ '^[a-z][a-z0-9_-]{0,62}$'
  AND jsonb_typeof(source->'compose'->'container_port')='number' AND (source->'compose'->>'container_port')::numeric BETWEEN 1 AND 65535
  AND (source->'compose'->>'container_port')::numeric=trunc((source->'compose'->>'container_port')::numeric))
) IS TRUE);

-- Preserve each original source and its normalization under the resource CAS.
CREATE TABLE hosting_compose_source_versions (
 resource_id text NOT NULL REFERENCES resources(id) ON DELETE RESTRICT,
 generation bigint NOT NULL CHECK(generation>0),
 original text NOT NULL CHECK(octet_length(original) BETWEEN 1 AND 131072),
 original_hash text NOT NULL CHECK(original_hash ~ '^sha256:[0-9a-f]{64}$'),
 definition jsonb NOT NULL CHECK(jsonb_typeof(definition)='object' AND octet_length(definition::text)<=262144),
 definition_hash text NOT NULL CHECK(definition_hash ~ '^sha256:[0-9a-f]{64}$'),
 PRIMARY KEY(resource_id,generation)
);
CREATE TRIGGER hosting_compose_source_versions_immutable BEFORE UPDATE OR DELETE ON hosting_compose_source_versions FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_target_immutable();

CREATE FUNCTION nerocd_hosting_compose_source_consistent() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE resource_key text; desired resources;
BEGIN
 IF TG_TABLE_NAME='resources' THEN resource_key:=NEW.id; ELSE resource_key:=NEW.resource_id; END IF;
 SELECT * INTO desired FROM resources WHERE id=resource_key;
 IF desired.source->>'kind'='compose' THEN
  IF NOT EXISTS(SELECT 1 FROM hosting_compose_source_versions v WHERE v.resource_id=resource_key AND v.generation=desired.generation
   AND v.original=desired.source->'compose'->>'original' AND v.definition->>'version'='managed-compose.v1'
   AND v.definition->>'original_hash'=v.original_hash
   AND v.definition->>'public_component'=desired.source->'compose'->>'public_component'
   AND v.definition->'container_port'=desired.source->'compose'->'container_port') THEN
   RAISE EXCEPTION 'Compose source requires matching immutable normalization' USING ERRCODE='23514';
  END IF;
 ELSIF TG_TABLE_NAME<>'resources' THEN
  RAISE EXCEPTION 'Compose source version requires Compose resource' USING ERRCODE='23514';
 END IF;
 RETURN NULL;
END;$$;
CREATE CONSTRAINT TRIGGER resources_compose_source AFTER INSERT OR UPDATE ON resources DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_compose_source_consistent();
CREATE CONSTRAINT TRIGGER hosting_compose_source_consistency AFTER INSERT ON hosting_compose_source_versions DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_compose_source_consistent();
DO $migration$
DECLARE owner_schema text:=current_schema();
BEGIN
 EXECUTE format('ALTER FUNCTION %I.nerocd_hosting_compose_source_consistent() SET search_path TO pg_catalog, %I, pg_temp',owner_schema,owner_schema);
END;$migration$;

-- A request identity is consumed in the same transaction as the first resource.
CREATE TABLE hosting_compose_create_receipts (
 project_id text NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
 request_id text NOT NULL CHECK(request_id ~ '^[0-9a-f]{32}$'),
 resource_id text NOT NULL UNIQUE REFERENCES resources(id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
 request_hash text NOT NULL CHECK(request_hash ~ '^sha256:[0-9a-f]{64}$'),
 PRIMARY KEY(project_id,request_id)
);
CREATE TRIGGER hosting_compose_create_receipts_immutable BEFORE UPDATE OR DELETE ON hosting_compose_create_receipts FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_target_immutable();
