-- Database metadata creation is distinct from engine execution qualification.
ALTER TABLE resources DROP CONSTRAINT resources_kind_check;
ALTER TABLE resources ADD CONSTRAINT resources_kind_check CHECK(kind IN ('application','database'));

CREATE TABLE hosting_databases (
 resource_id text PRIMARY KEY,
 project_id text NOT NULL,
 binding_id text NOT NULL UNIQUE,
 destination_id text NOT NULL,
 storage_id text NOT NULL UNIQUE REFERENCES hosting_storage(id) DEFERRABLE INITIALLY DEFERRED,
 engine text NOT NULL,
 recipe_version text NOT NULL,
 image_reference text NOT NULL,
 runtime_state text NOT NULL DEFAULT 'adapter_unqualified' CHECK(runtime_state='adapter_unqualified'),
 created_by text NOT NULL REFERENCES principals(id),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(resource_id,project_id) REFERENCES resources(id,project_id) ON DELETE RESTRICT,
 FOREIGN KEY(binding_id,resource_id,destination_id) REFERENCES resource_bindings(id,resource_id,destination_id) ON DELETE RESTRICT,
 CHECK((engine='postgresql' AND recipe_version='postgresql-17.11-bookworm.v1' AND image_reference='docker.io/library/postgres@sha256:858bb5e98eed02eb8cafbcdc007f9a65d1b613972028944a8ec2133ccaf80d27') OR (engine='redis' AND recipe_version='redis-8.2.10-bookworm.v1' AND image_reference='docker.io/library/redis@sha256:e5f23377739f95a2f530b9c99f93accb4f634577f09d00a7d8e0638f2fdf69e0'))
);

CREATE TABLE hosting_database_credentials (
 resource_id text NOT NULL REFERENCES hosting_databases(resource_id) ON DELETE RESTRICT,
 role text NOT NULL CHECK(role IN ('admin','app','probe')),
 username text NOT NULL CHECK(username IN ('postgres','ncd_app','ncd_probe')),
 secret_id text NOT NULL UNIQUE REFERENCES project_managed_secrets(id) ON DELETE RESTRICT,
 version bigint NOT NULL CHECK(version=1),
 PRIMARY KEY(resource_id,role),
 FOREIGN KEY(secret_id,version) REFERENCES project_managed_secret_versions(secret_id,version) ON DELETE RESTRICT
);

CREATE TABLE hosting_database_receipts (
 project_id text NOT NULL REFERENCES projects(id),
 request_id text NOT NULL CHECK(request_id ~ '^[0-9a-f]{32}$'),
 resource_id text NOT NULL UNIQUE REFERENCES hosting_databases(resource_id),
 actor_id text NOT NULL REFERENCES principals(id),
 request_hash text NOT NULL CHECK(request_hash ~ '^sha256:[0-9a-f]{64}$'),
 accepted_request jsonb NOT NULL CHECK(jsonb_typeof(accepted_request)='object' AND octet_length(accepted_request::text)<=4096),
 audit_id text NOT NULL UNIQUE REFERENCES audit_events(id) DEFERRABLE INITIALLY DEFERRED,
 PRIMARY KEY(project_id,request_id)
);

CREATE FUNCTION nerocd_hosting_database_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'database metadata and credential ownership are immutable' USING ERRCODE='23514'; END;
$$;
CREATE TRIGGER hosting_databases_immutable BEFORE UPDATE OR DELETE ON hosting_databases FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_database_immutable();
CREATE TRIGGER hosting_database_credentials_immutable BEFORE UPDATE OR DELETE ON hosting_database_credentials FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_database_immutable();
CREATE TRIGGER hosting_database_receipts_immutable BEFORE UPDATE OR DELETE ON hosting_database_receipts FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_database_immutable();

CREATE FUNCTION nerocd_hosting_database_validate(resource_key text) RETURNS void LANGUAGE plpgsql AS $$
DECLARE d hosting_databases; r resources; b resource_bindings; s hosting_storage; receipt hosting_database_receipts; credential record; credential_count integer;
BEGIN
 SELECT * INTO STRICT d FROM hosting_databases WHERE resource_id=resource_key;
 SELECT * INTO STRICT r FROM resources WHERE id=d.resource_id;
 SELECT * INTO STRICT b FROM resource_bindings WHERE id=d.binding_id;
 SELECT * INTO STRICT s FROM hosting_storage WHERE id=d.storage_id;
 SELECT * INTO STRICT receipt FROM hosting_database_receipts WHERE resource_id=d.resource_id;
 IF EXISTS(SELECT 1 FROM jsonb_each(receipt.accepted_request) field WHERE jsonb_typeof(field.value)<>'string') THEN
  RAISE EXCEPTION 'database accepted request must contain string fields' USING ERRCODE='23514';
 END IF;
 IF r.kind<>'database' OR r.generation<>1 OR r.source IS DISTINCT FROM jsonb_build_object('kind','image','image',jsonb_build_object('reference',d.image_reference)) OR r.project_id<>d.project_id OR r.created_by<>d.created_by OR b.resource_id<>r.id OR b.destination_id<>d.destination_id OR b.created_by<>d.created_by OR s.resource_id<>r.id OR s.project_id<>d.project_id OR s.binding_id<>b.id OR s.destination_id<>d.destination_id OR s.created_by<>d.created_by OR s.mount_path<>(CASE d.engine WHEN 'postgresql' THEN '/var/lib/postgresql/data' ELSE '/data' END) OR receipt.project_id<>d.project_id OR receipt.actor_id<>d.created_by THEN
  RAISE EXCEPTION 'database connected metadata mismatch' USING ERRCODE='23514';
 END IF;
 IF receipt.accepted_request->>'project_id' IS DISTINCT FROM d.project_id OR receipt.accepted_request->>'request_id' IS DISTINCT FROM receipt.request_id OR receipt.accepted_request->>'project_environment_id' IS DISTINCT FROM r.project_environment_id OR receipt.accepted_request->>'name' IS DISTINCT FROM r.name OR receipt.accepted_request->>'destination_id' IS DISTINCT FROM d.destination_id OR receipt.accepted_request->>'engine' IS DISTINCT FROM d.engine OR receipt.accepted_request->>'recipe_version' IS DISTINCT FROM d.recipe_version OR (SELECT count(*) FROM jsonb_object_keys(receipt.accepted_request))<>7 THEN
  RAISE EXCEPTION 'database accepted request mismatch' USING ERRCODE='23514';
 END IF;
 PERFORM 1 FROM hosting_storage_receipts sr WHERE sr.storage_id=s.id AND sr.resource_id=r.id AND sr.actor_id=d.created_by AND sr.request_id=receipt.request_id AND sr.expected_resource_generation=1;
 IF NOT FOUND THEN RAISE EXCEPTION 'database storage receipt missing' USING ERRCODE='23514'; END IF;
 PERFORM 1 FROM destination_project_grants WHERE destination_id=d.destination_id AND project_id=d.project_id AND revoked_at IS NULL FOR SHARE;
 IF NOT FOUND THEN RAISE EXCEPTION 'database destination grant unavailable' USING ERRCODE='23514'; END IF;
 PERFORM 1 FROM audit_events a WHERE a.actor_id=d.created_by AND a.action='resource.created' AND a.target_id=r.id AND a.metadata->>'project_id'=d.project_id AND a.metadata->>'project_environment_id'=r.project_environment_id AND a.metadata->>'generation'='1';
 IF NOT FOUND THEN RAISE EXCEPTION 'database resource audit missing' USING ERRCODE='23514'; END IF;
 PERFORM 1 FROM audit_events a WHERE a.actor_id=d.created_by AND a.action='resource.destination_bound' AND a.target_id=r.id AND a.metadata->>'project_id'=d.project_id AND a.metadata->>'destination_id'=d.destination_id AND a.metadata->>'binding_id'=b.id;
 IF NOT FOUND THEN RAISE EXCEPTION 'database binding audit missing' USING ERRCODE='23514'; END IF;
 SELECT count(*) INTO credential_count FROM hosting_database_credentials WHERE resource_id=r.id;
 IF credential_count<>2 THEN RAISE EXCEPTION 'database requires two distinct credentials' USING ERRCODE='23514'; END IF;
 FOR credential IN SELECT c.*,m.project_id,m.current_version,m.status,m.created_by FROM hosting_database_credentials c JOIN project_managed_secrets m ON m.id=c.secret_id WHERE c.resource_id=r.id LOOP
  IF credential.project_id<>d.project_id OR credential.current_version<>1 OR credential.status<>'active' OR credential.created_by<>d.created_by OR NOT ((d.engine='postgresql' AND ((credential.role='admin' AND credential.username='postgres') OR (credential.role='app' AND credential.username='ncd_app'))) OR (d.engine='redis' AND ((credential.role='app' AND credential.username='ncd_app') OR (credential.role='probe' AND credential.username='ncd_probe')))) THEN
   RAISE EXCEPTION 'database credential scope mismatch' USING ERRCODE='23514';
  END IF;
  PERFORM 1 FROM project_managed_secret_versions v WHERE v.secret_id=credential.secret_id AND v.version=1 AND v.created_by=d.created_by;
  IF NOT FOUND THEN RAISE EXCEPTION 'database credential version missing' USING ERRCODE='23514'; END IF;
  PERFORM 1 FROM audit_events a WHERE a.actor_id=d.created_by AND a.action='managed_secret.created' AND a.target_id=credential.secret_id AND a.metadata->>'project_id'=d.project_id AND a.metadata->>'version'='1';
  IF NOT FOUND THEN RAISE EXCEPTION 'database credential audit missing' USING ERRCODE='23514'; END IF;
 END LOOP;
 PERFORM 1 FROM audit_events a WHERE a.id=receipt.audit_id AND a.actor_id=d.created_by AND a.action='hosting.database.created' AND a.target_id=r.id AND a.metadata->>'project_id'=d.project_id AND a.metadata->>'binding_id'=b.id AND a.metadata->>'storage_id'=s.id AND a.metadata->>'engine'=d.engine AND a.metadata->>'recipe_version'=d.recipe_version;
 IF NOT FOUND THEN RAISE EXCEPTION 'database creation audit mismatch' USING ERRCODE='23514'; END IF;
END;
$$;

CREATE FUNCTION nerocd_hosting_database_consistent() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE resource_key text;
BEGIN
 IF TG_TABLE_NAME='resources' THEN
  IF NEW.kind<>'database' THEN RETURN NULL; END IF;
  resource_key:=NEW.id;
 ELSE resource_key:=NEW.resource_id;
 END IF;
 PERFORM nerocd_hosting_database_validate(resource_key);
 RETURN NULL;
END;
$$;
CREATE CONSTRAINT TRIGGER hosting_database_resource_consistency AFTER INSERT ON resources DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_database_consistent();
CREATE CONSTRAINT TRIGGER hosting_database_metadata_consistency AFTER INSERT ON hosting_databases DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_database_consistent();
CREATE CONSTRAINT TRIGGER hosting_database_credential_consistency AFTER INSERT ON hosting_database_credentials DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_database_consistent();
CREATE CONSTRAINT TRIGGER hosting_database_receipt_consistency AFTER INSERT ON hosting_database_receipts DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_database_consistent();

CREATE OR REPLACE FUNCTION nerocd_hosting_resource_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.kind='database' THEN RAISE EXCEPTION 'database configuration requires a qualified engine operation' USING ERRCODE='23514'; END IF;
 IF NEW.id IS DISTINCT FROM OLD.id OR NEW.project_id IS DISTINCT FROM OLD.project_id OR NEW.project_environment_id IS DISTINCT FROM OLD.project_environment_id OR NEW.kind IS DISTINCT FROM OLD.kind OR NEW.created_by IS DISTINCT FROM OLD.created_by OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
  RAISE EXCEPTION 'resource identity is immutable' USING ERRCODE='23514';
 END IF;
 IF NEW.generation<>OLD.generation+1 THEN RAISE EXCEPTION 'resource generation must advance once' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION nerocd_hosting_storage_creation_consistent() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE storage hosting_storage; receipt hosting_storage_receipts; resource resources;
BEGIN
 SELECT * INTO STRICT storage FROM hosting_storage WHERE resource_id=NEW.resource_id;
 SELECT * INTO STRICT receipt FROM hosting_storage_receipts WHERE storage_id=storage.id;
 SELECT * INTO STRICT resource FROM resources WHERE id=storage.resource_id FOR SHARE;
 IF resource.kind='database' THEN
  PERFORM nerocd_hosting_database_validate(resource.id);
 ELSIF resource.kind IS DISTINCT FROM 'application' THEN
  RAISE EXCEPTION 'invalid storage resource kind' USING ERRCODE='23514';
 END IF;
 IF resource.source->>'kind' IS DISTINCT FROM 'image' OR resource.generation<>receipt.expected_resource_generation OR receipt.resource_id<>storage.resource_id OR receipt.actor_id<>storage.created_by THEN
  RAISE EXCEPTION 'invalid storage resource or receipt' USING ERRCODE='23514';
 END IF;
 PERFORM 1 FROM destination_project_grants WHERE destination_id=storage.destination_id AND project_id=storage.project_id AND revoked_at IS NULL FOR SHARE;
 IF NOT FOUND THEN RAISE EXCEPTION 'storage destination grant unavailable' USING ERRCODE='23514'; END IF;
 IF NOT EXISTS(SELECT 1 FROM audit_events a WHERE a.id=receipt.audit_id AND a.actor_id=storage.created_by AND a.action='hosting.storage.created' AND a.target_id=storage.id AND a.metadata->>'project_id'=storage.project_id AND a.metadata->>'resource_id'=storage.resource_id AND a.metadata->>'binding_id'=storage.binding_id AND a.metadata->>'destination_id'=storage.destination_id AND a.metadata->>'storage_hash'=storage.body_hash AND a.metadata->>'storage_generation'='1') THEN
  RAISE EXCEPTION 'storage audit mismatch' USING ERRCODE='23514';
 END IF;
 RETURN NULL;
END;
$$;

CREATE FUNCTION nerocd_hosting_database_secret_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE secret_key text;
BEGIN
 IF TG_TABLE_NAME='project_managed_secrets' THEN secret_key:=NEW.id; ELSE secret_key:=NEW.secret_id; END IF;
 IF EXISTS(SELECT 1 FROM hosting_database_credentials WHERE secret_id=secret_key) THEN
  RAISE EXCEPTION 'database credentials require a qualified engine operation' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER hosting_database_secret_update_guard BEFORE UPDATE ON project_managed_secrets FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_database_secret_guard();
CREATE TRIGGER hosting_database_secret_version_guard BEFORE INSERT ON project_managed_secret_versions FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_database_secret_guard();

CREATE FUNCTION nerocd_hosting_database_deployment_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF EXISTS(SELECT 1 FROM resources WHERE id=NEW.resource_id AND kind='database') THEN
  RAISE EXCEPTION 'database engine adapter is unqualified' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER hosting_database_deployment_guard BEFORE INSERT ON hosting_operations FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_database_deployment_guard();

CREATE FUNCTION nerocd_hosting_database_schema_compatible() RETURNS boolean LANGUAGE sql STABLE AS $$
SELECT to_regclass(current_schema()||'.hosting_databases') IS NOT NULL
AND to_regclass(current_schema()||'.hosting_database_credentials') IS NOT NULL
AND to_regclass(current_schema()||'.hosting_database_receipts') IS NOT NULL
AND NOT EXISTS (
 SELECT 1 FROM (VALUES ('resources','hosting_database_resource_consistency'),('hosting_databases','hosting_databases_immutable'),('hosting_databases','hosting_database_metadata_consistency'),('hosting_database_credentials','hosting_database_credentials_immutable'),('hosting_database_credentials','hosting_database_credential_consistency'),('hosting_database_receipts','hosting_database_receipts_immutable'),('hosting_database_receipts','hosting_database_receipt_consistency'),('project_managed_secrets','hosting_database_secret_update_guard'),('project_managed_secret_versions','hosting_database_secret_version_guard'),('hosting_operations','hosting_database_deployment_guard')) required(table_name,trigger_name)
 WHERE NOT EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass(current_schema()||'.'||required.table_name) AND tgname=required.trigger_name AND tgenabled IN ('O','A'))
)
AND (SELECT count(*) FROM pg_constraint WHERE conrelid=to_regclass(current_schema()||'.hosting_databases') AND contype='f' AND convalidated)=4
AND (SELECT count(*) FROM pg_constraint WHERE conrelid=to_regclass(current_schema()||'.hosting_database_credentials') AND contype='f' AND convalidated)=3
AND (SELECT count(*) FROM pg_constraint WHERE conrelid=to_regclass(current_schema()||'.hosting_database_receipts') AND contype='f' AND convalidated)=4;
$$;

DO $$ DECLARE owner_schema text:=current_schema(); routine text; BEGIN
 FOREACH routine IN ARRAY ARRAY['nerocd_hosting_database_immutable()','nerocd_hosting_database_validate(text)','nerocd_hosting_database_consistent()','nerocd_hosting_resource_update()','nerocd_hosting_storage_creation_consistent()','nerocd_hosting_database_secret_guard()','nerocd_hosting_database_deployment_guard()','nerocd_hosting_database_schema_compatible()'] LOOP
  EXECUTE format('ALTER FUNCTION %I.%s SET search_path=%I,pg_catalog,pg_temp',owner_schema,routine,owner_schema);
 END LOOP;
END $$;
