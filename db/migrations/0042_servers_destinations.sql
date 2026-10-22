CREATE TABLE servers (
 id text PRIMARY KEY,
 name text NOT NULL CHECK (octet_length(name) BETWEEN 1 AND 128 AND name = btrim(name) AND name !~ '[[:cntrl:]]'),
 runner_id text NOT NULL UNIQUE REFERENCES runners(id) ON DELETE RESTRICT,
 created_by text NOT NULL REFERENCES principals(id) ON DELETE RESTRICT,
 created_at timestamptz NOT NULL
);
CREATE TABLE destinations (
 id text PRIMARY KEY,
 server_id text NOT NULL UNIQUE REFERENCES servers(id) ON DELETE RESTRICT,
 name text NOT NULL CHECK (octet_length(name) BETWEEN 1 AND 128 AND name = btrim(name) AND name !~ '[[:cntrl:]]'),
 runtime_kind text NOT NULL CHECK (runtime_kind='docker_compose'),
 created_at timestamptz NOT NULL
);
CREATE TABLE destination_project_grants (
 destination_id text NOT NULL REFERENCES destinations(id) ON DELETE RESTRICT,
 project_id text NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
 granted_by text NOT NULL REFERENCES principals(id) ON DELETE RESTRICT,
 created_at timestamptz NOT NULL,
 revoked_at timestamptz,
 PRIMARY KEY(destination_id,project_id)
);
CREATE TABLE resource_bindings (
 id text PRIMARY KEY,
 resource_id text NOT NULL UNIQUE REFERENCES resources(id) ON DELETE RESTRICT,
 destination_id text NOT NULL REFERENCES destinations(id) ON DELETE RESTRICT,
 runtime_namespace text NOT NULL CHECK (runtime_namespace ~ '^ncd_[a-f0-9]{36}$'),
 created_by text NOT NULL REFERENCES principals(id) ON DELETE RESTRICT,
 created_at timestamptz NOT NULL,
 UNIQUE(destination_id,runtime_namespace)
);
CREATE INDEX servers_page_idx ON servers(created_at,id);
CREATE INDEX destinations_page_idx ON destinations(created_at,id);
CREATE INDEX destination_project_grants_project_idx ON destination_project_grants(project_id,destination_id) WHERE revoked_at IS NULL;
CREATE INDEX resource_bindings_destination_idx ON resource_bindings(destination_id,created_at,id);

CREATE FUNCTION nerocd_hosting_target_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 RAISE EXCEPTION 'hosting target identity is immutable' USING ERRCODE='23514';
END;
$$;
CREATE TRIGGER servers_immutable BEFORE UPDATE OR DELETE ON servers FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_target_immutable();
CREATE TRIGGER destinations_immutable BEFORE UPDATE OR DELETE ON destinations FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_target_immutable();
CREATE TRIGGER resource_bindings_immutable BEFORE UPDATE OR DELETE ON resource_bindings FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_target_immutable();
CREATE FUNCTION nerocd_destination_grant_identity() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'destination grant history is immutable' USING ERRCODE='23514'; END IF;
 IF NEW.destination_id IS DISTINCT FROM OLD.destination_id OR NEW.project_id IS DISTINCT FROM OLD.project_id THEN
 RAISE EXCEPTION 'destination grant identity is immutable' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER destination_grants_identity BEFORE UPDATE OR DELETE ON destination_project_grants FOR EACH ROW EXECUTE FUNCTION nerocd_destination_grant_identity();
CREATE FUNCTION nerocd_server_runner_admission() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE candidate runners%ROWTYPE;
BEGIN
 SELECT * INTO candidate FROM runners WHERE id=NEW.runner_id FOR SHARE;
 IF NOT FOUND OR candidate.status<>'active' OR candidate.token_hash='' OR NOT ('compose_deploy'=ANY(candidate.capabilities)) OR 'shell'=ANY(candidate.capabilities) THEN
 RAISE EXCEPTION 'server runner is not eligible' USING ERRCODE='23514'; END IF;
 PERFORM 1 FROM runner_enrollments WHERE runner_id=NEW.runner_id AND used_at IS NOT NULL AND revoked_at IS NULL FOR SHARE;
 IF NOT FOUND THEN RAISE EXCEPTION 'server runner is not enrolled' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER servers_runner_admission BEFORE INSERT ON servers FOR EACH ROW EXECUTE FUNCTION nerocd_server_runner_admission();
CREATE FUNCTION nerocd_resource_binding_admission() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE resource_project text;
BEGIN
 SELECT project_id INTO resource_project FROM resources WHERE id=NEW.resource_id;
 IF NOT FOUND THEN RAISE EXCEPTION 'resource does not exist' USING ERRCODE='23503'; END IF;
 -- Lock the grant so a concurrent revoke cannot pass between checking and insertion.
 PERFORM 1 FROM destination_project_grants WHERE destination_id=NEW.destination_id AND project_id=resource_project AND revoked_at IS NULL FOR SHARE;
 IF NOT FOUND THEN RAISE EXCEPTION 'resource project is not granted' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER resource_bindings_admission BEFORE INSERT ON resource_bindings FOR EACH ROW EXECUTE FUNCTION nerocd_resource_binding_admission();
CREATE FUNCTION nerocd_destinations_schema_compatible() RETURNS boolean LANGUAGE sql STABLE AS $$
SELECT to_regclass(current_schema() || '.servers') IS NOT NULL
AND to_regclass(current_schema() || '.destinations') IS NOT NULL
AND to_regclass(current_schema() || '.destination_project_grants') IS NOT NULL
AND to_regclass(current_schema() || '.resource_bindings') IS NOT NULL
AND to_regclass(current_schema() || '.destination_project_grants_project_idx') IS NOT NULL
AND NOT EXISTS (
 SELECT 1 FROM (VALUES ('servers','servers_runner_id_key'),('destinations','destinations_server_id_key'),('resource_bindings','resource_bindings_resource_id_key'),('resource_bindings','resource_bindings_destination_id_runtime_namespace_key'),('destination_project_grants','destination_project_grants_pkey')) AS required(table_name,constraint_name)
 WHERE NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid=to_regclass(current_schema() || '.' || required.table_name) AND conname=required.constraint_name AND contype IN ('u','p') AND convalidated)
)
AND NOT EXISTS (
 SELECT 1 FROM (VALUES ('servers','servers_runner_id_fkey'),('servers','servers_created_by_fkey'),('destinations','destinations_server_id_fkey'),('destination_project_grants','destination_project_grants_destination_id_fkey'),('destination_project_grants','destination_project_grants_project_id_fkey'),('destination_project_grants','destination_project_grants_granted_by_fkey'),('resource_bindings','resource_bindings_resource_id_fkey'),('resource_bindings','resource_bindings_destination_id_fkey'),('resource_bindings','resource_bindings_created_by_fkey')) AS required(table_name,constraint_name)
 WHERE NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid=to_regclass(current_schema() || '.' || required.table_name) AND conname=required.constraint_name AND contype='f' AND convalidated)
)
AND NOT EXISTS (
 SELECT 1 FROM (VALUES ('servers','servers_immutable'),('servers','servers_runner_admission'),('destinations','destinations_immutable'),('resource_bindings','resource_bindings_immutable'),('resource_bindings','resource_bindings_admission'),('destination_project_grants','destination_grants_identity')) AS required(table_name,trigger_name)
 WHERE NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass(current_schema() || '.' || required.table_name) AND tgname=required.trigger_name AND tgenabled IN ('O','A'))
);
$$;
