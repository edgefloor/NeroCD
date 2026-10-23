-- Desired local-volume metadata only. No Docker effect or public route is added.
ALTER TABLE resource_bindings ADD CONSTRAINT resource_bindings_storage_identity UNIQUE(id,resource_id,destination_id);

CREATE FUNCTION nerocd_hosting_storage_path_valid(value text) RETURNS boolean LANGUAGE sql IMMUTABLE AS $$
SELECT octet_length(value) BETWEEN 2 AND 512
AND value ~ '^/[A-Za-z0-9_./-]+$'
AND value !~ '//|/$|(^|/)\.\.?(/|$)'
AND value !~ '^/(proc|sys|dev)(/|$)';
$$;

CREATE TABLE hosting_storage (
 id text PRIMARY KEY,
 project_id text NOT NULL,
 resource_id text NOT NULL UNIQUE,
 binding_id text NOT NULL UNIQUE,
 destination_id text NOT NULL,
 kind text NOT NULL,
 driver text NOT NULL,
 volume_name text NOT NULL,
 mount_path text NOT NULL,
 access_mode text NOT NULL,
 retention text NOT NULL,
 generation bigint NOT NULL,
 body_hash text NOT NULL,
 created_by text NOT NULL REFERENCES principals(id),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 CONSTRAINT hosting_storage_identity CHECK(id ~ '^hsv_[0-9a-f]{32}$' AND volume_name='ncdv_'||substr(id,5) AND kind='platform_managed' AND driver='local' AND access_mode='read_write' AND retention='retain' AND generation=1 AND body_hash ~ '^sha256:[0-9a-f]{64}$'),
 CONSTRAINT hosting_storage_mount CHECK(nerocd_hosting_storage_path_valid(mount_path) IS TRUE),
 CONSTRAINT hosting_storage_resource_scope FOREIGN KEY(resource_id,project_id) REFERENCES resources(id,project_id) ON DELETE RESTRICT,
 CONSTRAINT hosting_storage_binding_scope FOREIGN KEY(binding_id,resource_id,destination_id) REFERENCES resource_bindings(id,resource_id,destination_id) ON DELETE RESTRICT,
 UNIQUE(destination_id,volume_name)
);
CREATE TABLE hosting_storage_receipts (
 resource_id text NOT NULL REFERENCES resources(id),
 request_id text NOT NULL CHECK(request_id ~ '^[0-9a-f]{32}$'),
 storage_id text NOT NULL UNIQUE REFERENCES hosting_storage(id),
 actor_id text NOT NULL REFERENCES principals(id),
 request_hash text NOT NULL CHECK(request_hash ~ '^sha256:[0-9a-f]{64}$'),
 expected_resource_generation bigint NOT NULL CHECK(expected_resource_generation>0),
 audit_id text NOT NULL UNIQUE REFERENCES audit_events(id) DEFERRABLE INITIALLY DEFERRED,
 PRIMARY KEY(resource_id,request_id)
);

CREATE FUNCTION nerocd_hosting_storage_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'hosting storage identity and receipts are retained and immutable' USING ERRCODE='23514'; END;
$$;
CREATE TRIGGER hosting_storage_immutable BEFORE UPDATE OR DELETE ON hosting_storage FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_storage_immutable();
CREATE TRIGGER hosting_storage_receipts_immutable BEFORE UPDATE OR DELETE ON hosting_storage_receipts FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_storage_immutable();

CREATE FUNCTION nerocd_hosting_storage_creation_consistent() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE storage hosting_storage%ROWTYPE; receipt hosting_storage_receipts%ROWTYPE; resource resources%ROWTYPE;
BEGIN
 SELECT * INTO STRICT storage FROM hosting_storage WHERE resource_id=NEW.resource_id;
 SELECT * INTO STRICT receipt FROM hosting_storage_receipts WHERE storage_id=storage.id;
 SELECT * INTO STRICT resource FROM resources WHERE id=storage.resource_id FOR SHARE;
 IF resource.kind IS DISTINCT FROM 'application' OR resource.source->>'kind' IS DISTINCT FROM 'image' OR resource.generation<>receipt.expected_resource_generation OR receipt.resource_id<>storage.resource_id OR receipt.actor_id<>storage.created_by THEN
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
CREATE CONSTRAINT TRIGGER hosting_storage_creation_consistency AFTER INSERT ON hosting_storage DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_storage_creation_consistent();
CREATE CONSTRAINT TRIGGER hosting_storage_receipt_consistency AFTER INSERT ON hosting_storage_receipts DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_storage_creation_consistent();

CREATE FUNCTION nerocd_hosting_storage_schema_compatible() RETURNS boolean LANGUAGE sql STABLE AS $$
SELECT to_regclass(current_schema()||'.hosting_storage') IS NOT NULL
AND to_regclass(current_schema()||'.hosting_storage_receipts') IS NOT NULL
AND NOT EXISTS (
 SELECT 1 FROM (VALUES ('hosting_storage','hosting_storage_pkey'),('hosting_storage','hosting_storage_created_by_fkey'),('hosting_storage','hosting_storage_identity'),('hosting_storage','hosting_storage_mount'),('hosting_storage','hosting_storage_resource_scope'),('hosting_storage','hosting_storage_binding_scope'),('hosting_storage','hosting_storage_resource_id_key'),('hosting_storage','hosting_storage_binding_id_key'),('hosting_storage','hosting_storage_destination_id_volume_name_key'),('hosting_storage_receipts','hosting_storage_receipts_pkey'),('hosting_storage_receipts','hosting_storage_receipts_resource_id_fkey'),('hosting_storage_receipts','hosting_storage_receipts_request_id_check'),('hosting_storage_receipts','hosting_storage_receipts_storage_id_key'),('hosting_storage_receipts','hosting_storage_receipts_storage_id_fkey'),('hosting_storage_receipts','hosting_storage_receipts_actor_id_fkey'),('hosting_storage_receipts','hosting_storage_receipts_request_hash_check'),('hosting_storage_receipts','hosting_storage_receipts_expected_resource_generation_check'),('hosting_storage_receipts','hosting_storage_receipts_audit_id_key'),('hosting_storage_receipts','hosting_storage_receipts_audit_id_fkey')) required(table_name,constraint_name)
 WHERE NOT EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid=to_regclass(current_schema()||'.'||required.table_name) AND conname=required.constraint_name AND convalidated)
)
AND NOT EXISTS (
 SELECT 1 FROM (VALUES ('hosting_storage','hosting_storage_immutable'),('hosting_storage_receipts','hosting_storage_receipts_immutable'),('hosting_storage','hosting_storage_creation_consistency'),('hosting_storage_receipts','hosting_storage_receipt_consistency')) required(table_name,trigger_name)
 WHERE NOT EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass(current_schema()||'.'||required.table_name) AND tgname=required.trigger_name AND tgenabled IN('O','A'))
);
$$;

DO $$ DECLARE owner_schema text:=current_schema(); routine text; BEGIN
 FOREACH routine IN ARRAY ARRAY['nerocd_hosting_storage_path_valid(text)','nerocd_hosting_storage_immutable()','nerocd_hosting_storage_creation_consistent()','nerocd_hosting_storage_schema_compatible()'] LOOP
  EXECUTE format('ALTER FUNCTION %I.%s SET search_path=%I,pg_catalog,pg_temp',owner_schema,routine,owner_schema);
 END LOOP;
END $$;
