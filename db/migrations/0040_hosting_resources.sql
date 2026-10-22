-- Shared environments belong to projects, independently of legacy deployment services.
CREATE TABLE project_environments (
 id text PRIMARY KEY,
 project_id text NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
 name text NOT NULL CHECK (octet_length(name) BETWEEN 1 AND 128 AND name = btrim(name) AND name !~ '[[:cntrl:]]'),
 created_at timestamptz NOT NULL,
 UNIQUE (project_id, name),
 UNIQUE (project_id, id)
);
CREATE TABLE resources (
 id text PRIMARY KEY,
 project_id text NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
 project_environment_id text NOT NULL,
 name text NOT NULL CHECK (octet_length(name) BETWEEN 1 AND 128 AND name = btrim(name) AND name !~ '[[:cntrl:]]'),
 kind text NOT NULL CHECK (kind = 'application'),
 source jsonb NOT NULL CHECK ((jsonb_typeof(source) = 'object' AND source->>'kind' = 'image' AND jsonb_typeof(source->'image') = 'object' AND jsonb_typeof(source->'image'->'reference') = 'string' AND length(source->'image'->>'reference') BETWEEN 1 AND 512) IS TRUE),
 generation bigint NOT NULL DEFAULT 1 CHECK (generation > 0),
 created_by text NOT NULL REFERENCES principals(id) ON DELETE RESTRICT,
 created_at timestamptz NOT NULL,
 updated_at timestamptz NOT NULL,
 UNIQUE (project_environment_id, name),
 CONSTRAINT resources_project_environment_fk FOREIGN KEY (project_id, project_environment_id) REFERENCES project_environments(project_id, id) ON DELETE RESTRICT
);
CREATE INDEX project_environments_page_idx ON project_environments(project_id, created_at, id);
CREATE INDEX resources_page_idx ON resources(project_id, created_at, id);
CREATE INDEX resources_environment_page_idx ON resources(project_id, project_environment_id, created_at, id);

CREATE FUNCTION nerocd_hosting_resource_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.id IS DISTINCT FROM OLD.id OR NEW.project_id IS DISTINCT FROM OLD.project_id OR NEW.project_environment_id IS DISTINCT FROM OLD.project_environment_id OR NEW.kind IS DISTINCT FROM OLD.kind OR NEW.created_by IS DISTINCT FROM OLD.created_by OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
  RAISE EXCEPTION 'resource identity is immutable' USING ERRCODE = '23514';
 END IF;
 IF NEW.generation <> OLD.generation + 1 THEN
  RAISE EXCEPTION 'resource generation must advance once' USING ERRCODE = '23514';
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER resources_immutable_identity BEFORE UPDATE ON resources FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_resource_update();

CREATE FUNCTION nerocd_hosting_schema_compatible() RETURNS boolean LANGUAGE sql STABLE AS $$
SELECT true
AND to_regclass(current_schema() || '.project_environments') IS NOT NULL
AND to_regclass(current_schema() || '.resources') IS NOT NULL
AND to_regclass(current_schema() || '.project_environments_page_idx') IS NOT NULL
AND to_regclass(current_schema() || '.resources_environment_page_idx') IS NOT NULL
AND EXISTS (SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass(current_schema() || '.resources') AND tgname='resources_immutable_identity' AND tgenabled <> 'D')
AND EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid=to_regclass(current_schema() || '.resources') AND conname='resources_project_environment_fk' AND contype='f' AND convalidated)
;
$$;
