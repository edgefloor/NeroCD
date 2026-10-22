-- Reference-only desired runtime configuration. Saving never starts execution.
CREATE TABLE hosting_runtime_configurations (
 resource_id text PRIMARY KEY,
 project_id text NOT NULL,
 generation bigint NOT NULL CHECK(generation>0),
 UNIQUE(resource_id,project_id),
 FOREIGN KEY(resource_id,project_id) REFERENCES resources(id,project_id) ON DELETE RESTRICT
);
CREATE TABLE hosting_runtime_configuration_versions (
 resource_id text NOT NULL,
 project_id text NOT NULL,
 generation bigint NOT NULL CHECK(generation>0),
 body jsonb NOT NULL CHECK(jsonb_typeof(body)='object' AND octet_length(body::text)<=524288),
 body_hash text NOT NULL CHECK(body_hash~'^sha256:[0-9a-f]{64}$'),
 created_by text NOT NULL REFERENCES principals(id),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(resource_id,generation),
 UNIQUE(resource_id,project_id,generation),
 FOREIGN KEY(resource_id,project_id) REFERENCES hosting_runtime_configurations(resource_id,project_id)
);
ALTER TABLE hosting_runtime_configurations ADD CONSTRAINT hosting_runtime_current_version_fk
FOREIGN KEY(resource_id,generation) REFERENCES hosting_runtime_configuration_versions(resource_id,generation) DEFERRABLE INITIALLY DEFERRED;
CREATE TABLE hosting_runtime_configuration_secret_refs (
 resource_id text NOT NULL,
 project_id text NOT NULL,
 generation bigint NOT NULL,
 secret_id text NOT NULL,
 secret_version bigint NOT NULL CHECK(secret_version>0),
 PRIMARY KEY(resource_id,generation,secret_id,secret_version),
 FOREIGN KEY(resource_id,project_id,generation) REFERENCES hosting_runtime_configuration_versions(resource_id,project_id,generation),
 FOREIGN KEY(secret_id,project_id) REFERENCES project_managed_secrets(id,project_id),
 FOREIGN KEY(secret_id,secret_version) REFERENCES project_managed_secret_versions(secret_id,version)
);
CREATE TABLE hosting_runtime_configuration_receipts (
 resource_id text NOT NULL,
 project_id text NOT NULL,
 request_id text NOT NULL CHECK(request_id~'^[0-9a-f]{32}$'),
 actor_id text NOT NULL REFERENCES principals(id),
 request_hash text NOT NULL CHECK(request_hash~'^sha256:[0-9a-f]{64}$'),
 expected_resource_generation bigint NOT NULL CHECK(expected_resource_generation>0),
 expected_runtime_generation bigint NOT NULL CHECK(expected_runtime_generation>=0 AND expected_runtime_generation<9223372036854775807),
 runtime_generation bigint NOT NULL CHECK(runtime_generation>0 AND runtime_generation=expected_runtime_generation+1),
 audit_id text NOT NULL UNIQUE REFERENCES audit_events(id) DEFERRABLE INITIALLY DEFERRED,
 PRIMARY KEY(resource_id,request_id),
 UNIQUE(resource_id,runtime_generation),
 FOREIGN KEY(resource_id,project_id,runtime_generation) REFERENCES hosting_runtime_configuration_versions(resource_id,project_id,generation)
);

CREATE FUNCTION nerocd_hosting_runtime_document_valid(body jsonb) RETURNS boolean LANGUAGE plpgsql IMMUTABLE AS $$
DECLARE entry jsonb; ref jsonb; name text; names text[]:='{}'; selected bigint;
BEGIN
 IF jsonb_typeof(body) IS DISTINCT FROM 'object' OR (SELECT count(*) FROM jsonb_object_keys(body))<>1 OR jsonb_typeof(body->'entries') IS DISTINCT FROM 'array' OR jsonb_array_length(body->'entries')>64 THEN RETURN false; END IF;
 FOR entry IN SELECT value FROM jsonb_array_elements(body->'entries') LOOP
  IF jsonb_typeof(entry) IS DISTINCT FROM 'object' OR (SELECT count(*) FROM jsonb_object_keys(entry))<>3 OR jsonb_typeof(entry->'name') IS DISTINCT FROM 'string' OR jsonb_typeof(entry->'kind') IS DISTINCT FROM 'string' THEN RETURN false; END IF;
  name:=entry->>'name';
  IF name!~'^[A-Za-z_][A-Za-z0-9_]{0,127}$' OR left(name,7)='NEROCD_' OR name=ANY(names) THEN RETURN false; END IF;
  names:=array_append(names,name);
  IF entry->>'kind'='literal' THEN
   IF NOT entry ? 'value' OR jsonb_typeof(entry->'value') IS DISTINCT FROM 'string' OR octet_length(entry->>'value')>4096 OR strpos(entry->>'value',chr(10))>0 OR strpos(entry->>'value',chr(13))>0 THEN RETURN false; END IF;
  ELSIF entry->>'kind'='managed_secret' THEN
   ref:=entry->'secret';
   IF jsonb_typeof(ref) IS DISTINCT FROM 'object' OR (SELECT count(*) FROM jsonb_object_keys(ref))<>2 OR jsonb_typeof(ref->'secret_id') IS DISTINCT FROM 'string' OR jsonb_typeof(ref->'version') IS DISTINCT FROM 'number' THEN RETURN false; END IF;
   IF octet_length(ref->>'secret_id') NOT BETWEEN 1 AND 128 OR ref->>'secret_id'<>btrim(ref->>'secret_id') OR ref->>'secret_id'~'[[:cntrl:]]' THEN RETURN false; END IF;
   selected:=(ref->>'version')::bigint;IF selected<1 THEN RETURN false; END IF;
  ELSE RETURN false;
  END IF;
 END LOOP;
 IF (SELECT count(*) FROM (SELECT DISTINCT value->'secret' FROM jsonb_array_elements(body->'entries') WHERE value->>'kind'='managed_secret') refs)>16 THEN RETURN false; END IF;
 RETURN true;
EXCEPTION WHEN invalid_text_representation OR numeric_value_out_of_range OR invalid_parameter_value THEN RETURN false;
END;
$$;
ALTER TABLE hosting_runtime_configuration_versions ADD CONSTRAINT hosting_runtime_document_shape CHECK(nerocd_hosting_runtime_document_valid(body) IS TRUE);

CREATE FUNCTION nerocd_hosting_runtime_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'hosting runtime history is immutable' USING ERRCODE='23514'; END;
$$;
CREATE TRIGGER hosting_runtime_versions_immutable BEFORE UPDATE OR DELETE ON hosting_runtime_configuration_versions FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_runtime_immutable();
CREATE TRIGGER hosting_runtime_refs_immutable BEFORE UPDATE OR DELETE ON hosting_runtime_configuration_secret_refs FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_runtime_immutable();
CREATE TRIGGER hosting_runtime_receipts_immutable BEFORE UPDATE OR DELETE ON hosting_runtime_configuration_receipts FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_runtime_immutable();

CREATE FUNCTION nerocd_hosting_runtime_pointer_transition() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='INSERT' THEN
  IF NEW.generation<>1 THEN RAISE EXCEPTION 'runtime configuration must start at generation one' USING ERRCODE='23514'; END IF;
 ELSIF TG_OP='UPDATE' THEN
  IF NEW.resource_id IS DISTINCT FROM OLD.resource_id OR NEW.project_id IS DISTINCT FROM OLD.project_id OR OLD.generation=9223372036854775807 OR NEW.generation<>OLD.generation+1 THEN RAISE EXCEPTION 'invalid runtime configuration transition' USING ERRCODE='23514'; END IF;
 ELSE RAISE EXCEPTION 'runtime configuration history is retained' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER hosting_runtime_pointer_transition BEFORE INSERT OR UPDATE OR DELETE ON hosting_runtime_configurations FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_runtime_pointer_transition();

-- A reference may be inserted only while its immutable version is being created.
CREATE FUNCTION nerocd_hosting_runtime_ref_creation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NOT EXISTS(SELECT 1 FROM hosting_runtime_configuration_versions v WHERE v.resource_id=NEW.resource_id AND v.generation=NEW.generation AND v.xmin::text::bigint=mod(txid_current(),4294967296)) THEN RAISE EXCEPTION 'runtime references are sealed with their version' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER hosting_runtime_ref_creation BEFORE INSERT ON hosting_runtime_configuration_secret_refs FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_runtime_ref_creation();

-- Deferred checks bind the immutable document, normalized references, receipt,
-- pointer and sanitized audit in the transaction that creates the version.
CREATE FUNCTION nerocd_hosting_runtime_version_consistent() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE version hosting_runtime_configuration_versions%ROWTYPE; receipt hosting_runtime_configuration_receipts%ROWTYPE; ref record;
BEGIN
 SELECT * INTO STRICT version FROM hosting_runtime_configuration_versions WHERE resource_id=NEW.resource_id AND generation=NEW.generation;
 IF NOT EXISTS(SELECT 1 FROM hosting_runtime_configurations c WHERE c.resource_id=version.resource_id AND c.project_id=version.project_id AND c.generation=version.generation) THEN RAISE EXCEPTION 'runtime version must be current when created' USING ERRCODE='23514'; END IF;
 IF EXISTS(
  (SELECT DISTINCT value->'secret'->>'secret_id',(value->'secret'->>'version')::bigint FROM jsonb_array_elements(version.body->'entries') WHERE value->>'kind'='managed_secret'
   EXCEPT SELECT secret_id,secret_version FROM hosting_runtime_configuration_secret_refs WHERE resource_id=version.resource_id AND generation=version.generation)
  UNION ALL
  (SELECT secret_id,secret_version FROM hosting_runtime_configuration_secret_refs WHERE resource_id=version.resource_id AND generation=version.generation
   EXCEPT SELECT DISTINCT value->'secret'->>'secret_id',(value->'secret'->>'version')::bigint FROM jsonb_array_elements(version.body->'entries') WHERE value->>'kind'='managed_secret')
 ) THEN RAISE EXCEPTION 'runtime reference document mismatch' USING ERRCODE='23514'; END IF;
 FOR ref IN SELECT secret_id,secret_version FROM hosting_runtime_configuration_secret_refs WHERE resource_id=version.resource_id AND generation=version.generation ORDER BY secret_id,secret_version LOOP
  PERFORM s.id FROM project_managed_secrets s JOIN project_managed_secret_versions v ON v.secret_id=s.id WHERE s.id=ref.secret_id AND s.project_id=version.project_id AND s.status='active' AND v.version=ref.secret_version FOR SHARE OF s;
  IF NOT FOUND THEN RAISE EXCEPTION 'runtime secret reference is unavailable' USING ERRCODE='23514'; END IF;
 END LOOP;
 SELECT * INTO STRICT receipt FROM hosting_runtime_configuration_receipts WHERE resource_id=version.resource_id AND runtime_generation=version.generation;
 IF receipt.project_id<>version.project_id OR receipt.actor_id<>version.created_by OR NOT EXISTS(SELECT 1 FROM audit_events a WHERE a.id=receipt.audit_id AND a.actor_id=receipt.actor_id AND a.action='hosting.runtime_configuration.updated' AND a.target_id=version.resource_id AND a.metadata->>'project_id'=version.project_id AND a.metadata->>'resource_id'=version.resource_id AND (a.metadata->>'runtime_generation')::bigint=version.generation) THEN RAISE EXCEPTION 'runtime receipt audit mismatch' USING ERRCODE='23514'; END IF;
 RETURN NULL;
END;
$$;
CREATE CONSTRAINT TRIGGER hosting_runtime_version_consistency AFTER INSERT ON hosting_runtime_configuration_versions DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_runtime_version_consistent();
CREATE CONSTRAINT TRIGGER hosting_runtime_ref_consistency AFTER INSERT ON hosting_runtime_configuration_secret_refs DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_runtime_version_consistent();

CREATE FUNCTION nerocd_hosting_runtime_configuration_schema_compatible() RETURNS boolean LANGUAGE sql STABLE AS $$
SELECT to_regclass(current_schema()||'.hosting_runtime_configurations') IS NOT NULL
AND to_regclass(current_schema()||'.hosting_runtime_configuration_versions') IS NOT NULL
AND to_regclass(current_schema()||'.hosting_runtime_configuration_secret_refs') IS NOT NULL
AND to_regclass(current_schema()||'.hosting_runtime_configuration_receipts') IS NOT NULL
AND EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid=to_regclass(current_schema()||'.hosting_runtime_configurations') AND conname='hosting_runtime_current_version_fk' AND contype='f' AND convalidated)
AND EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid=to_regclass(current_schema()||'.hosting_runtime_configuration_versions') AND conname='hosting_runtime_document_shape' AND contype='c' AND convalidated)
AND EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass(current_schema()||'.hosting_runtime_configuration_versions') AND tgname='hosting_runtime_versions_immutable' AND tgenabled IN('O','A'))
AND EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass(current_schema()||'.hosting_runtime_configuration_versions') AND tgname='hosting_runtime_version_consistency' AND tgenabled IN('O','A'))
AND EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass(current_schema()||'.hosting_runtime_configuration_secret_refs') AND tgname='hosting_runtime_ref_consistency' AND tgenabled IN('O','A'))
AND EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass(current_schema()||'.hosting_runtime_configuration_secret_refs') AND tgname='hosting_runtime_ref_creation' AND tgenabled IN('O','A'))
AND EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass(current_schema()||'.hosting_runtime_configuration_secret_refs') AND tgname='hosting_runtime_refs_immutable' AND tgenabled IN('O','A'))
AND EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass(current_schema()||'.hosting_runtime_configuration_receipts') AND tgname='hosting_runtime_receipts_immutable' AND tgenabled IN('O','A'))
AND EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass(current_schema()||'.hosting_runtime_configurations') AND tgname='hosting_runtime_pointer_transition' AND tgenabled IN('O','A'));
$$;

-- Bind trigger and readiness queries to the owning schema, including restores.
DO $$ DECLARE owner_schema text:=current_schema(); routine text; BEGIN
 FOREACH routine IN ARRAY ARRAY['nerocd_hosting_runtime_document_valid(jsonb)','nerocd_hosting_runtime_immutable()','nerocd_hosting_runtime_pointer_transition()','nerocd_hosting_runtime_ref_creation()','nerocd_hosting_runtime_version_consistent()','nerocd_hosting_runtime_configuration_schema_compatible()'] LOOP
  EXECUTE format('ALTER FUNCTION %I.%s SET search_path=%I,pg_catalog',owner_schema,routine,owner_schema);
 END LOOP;
END $$;
