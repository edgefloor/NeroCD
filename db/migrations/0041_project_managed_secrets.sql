CREATE TABLE project_managed_secrets (
 id text PRIMARY KEY,
 project_id text NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
 name text NOT NULL CHECK (octet_length(name) BETWEEN 1 AND 128 AND name = btrim(name) AND name !~ '[[:cntrl:]]'),
 current_version bigint NOT NULL CHECK (current_version > 0),
 status text NOT NULL CHECK (status IN ('active','revoked')),
 created_by text NOT NULL REFERENCES principals(id) ON DELETE RESTRICT,
 created_at timestamptz NOT NULL,
 updated_at timestamptz NOT NULL,
 revoked_at timestamptz,
 CONSTRAINT managed_secrets_revoked_state CHECK ((status = 'revoked') = (revoked_at IS NOT NULL)),
 UNIQUE(project_id,name)
);
CREATE TABLE project_managed_secret_versions (
 secret_id text NOT NULL REFERENCES project_managed_secrets(id) ON DELETE RESTRICT,
 version bigint NOT NULL CHECK (version > 0),
 envelope_version integer NOT NULL CHECK (envelope_version = 1),
 key_id text NOT NULL CHECK (key_id ~ '^[A-Za-z0-9._-]{1,128}$'),
 ciphertext bytea NOT NULL CHECK (octet_length(ciphertext) BETWEEN 29 AND 65564),
 created_by text NOT NULL REFERENCES principals(id) ON DELETE RESTRICT,
 created_at timestamptz NOT NULL,
 PRIMARY KEY(secret_id,version)
);
ALTER TABLE project_managed_secrets ADD CONSTRAINT managed_secrets_current_version_fk FOREIGN KEY(id,current_version) REFERENCES project_managed_secret_versions(secret_id,version) DEFERRABLE INITIALLY DEFERRED;
CREATE INDEX managed_secrets_page_idx ON project_managed_secrets(project_id,created_at,id);
CREATE FUNCTION nerocd_managed_secret_version_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 RAISE EXCEPTION 'managed secret version is immutable' USING ERRCODE = '23514';
END;
$$;
CREATE TRIGGER managed_secret_versions_immutable BEFORE UPDATE OR DELETE ON project_managed_secret_versions FOR EACH ROW EXECUTE FUNCTION nerocd_managed_secret_version_immutable();
CREATE FUNCTION nerocd_managed_secret_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.id IS DISTINCT FROM OLD.id OR NEW.project_id IS DISTINCT FROM OLD.project_id OR NEW.name IS DISTINCT FROM OLD.name OR NEW.created_by IS DISTINCT FROM OLD.created_by OR NEW.created_at IS DISTINCT FROM OLD.created_at OR OLD.status = 'revoked' THEN
  RAISE EXCEPTION 'managed secret identity and revocation are immutable' USING ERRCODE = '23514';
 END IF;
 IF NOT ((NEW.status = 'active' AND OLD.current_version < 9223372036854775807 AND NEW.current_version = OLD.current_version + 1 AND NEW.revoked_at IS NULL) OR (NEW.status = 'revoked' AND NEW.current_version = OLD.current_version AND NEW.revoked_at IS NOT NULL)) THEN
  RAISE EXCEPTION 'invalid managed secret transition' USING ERRCODE = '23514';
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER managed_secrets_transition BEFORE UPDATE ON project_managed_secrets FOR EACH ROW EXECUTE FUNCTION nerocd_managed_secret_update();
CREATE FUNCTION nerocd_managed_secrets_schema_compatible() RETURNS boolean LANGUAGE sql STABLE AS $$
SELECT to_regclass(current_schema() || '.project_managed_secrets') IS NOT NULL
AND to_regclass(current_schema() || '.project_managed_secret_versions') IS NOT NULL
AND to_regclass(current_schema() || '.managed_secrets_page_idx') IS NOT NULL
AND EXISTS (SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass(current_schema() || '.project_managed_secret_versions') AND tgname='managed_secret_versions_immutable' AND tgenabled IN ('O','A'))
AND EXISTS (SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass(current_schema() || '.project_managed_secrets') AND tgname='managed_secrets_transition' AND tgenabled IN ('O','A'))
AND EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid=to_regclass(current_schema() || '.project_managed_secrets') AND conname='managed_secrets_current_version_fk' AND contype='f' AND convalidated)
;
$$;
