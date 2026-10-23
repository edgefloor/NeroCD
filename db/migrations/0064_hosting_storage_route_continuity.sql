-- Controller reservations are independent of proxy mutation ownership and runner
-- receipts. The immutable descriptor is hashed into the deployment snapshot.
CREATE TABLE hosting_storage_route_continuities (
 operation_id text PRIMARY KEY REFERENCES hosting_operations(id) ON DELETE RESTRICT,
 proxy_id text NOT NULL REFERENCES hosting_proxies(id) ON DELETE RESTRICT,
 descriptor jsonb NOT NULL CHECK(jsonb_typeof(descriptor)='object' AND octet_length(descriptor::text)<=24576),
 descriptor_hash text NOT NULL CHECK(descriptor_hash ~ '^sha256:[0-9a-f]{64}$'),
 accepted_request jsonb NOT NULL CHECK(jsonb_typeof(accepted_request)='object'),
 state text NOT NULL DEFAULT 'reserved' CHECK(state IN ('reserved','unresolved','proxy_pending','loaded','proxy_failed','released')),
 successor_proxy_operation_id text REFERENCES hosting_proxy_operations(id) ON DELETE RESTRICT,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 CHECK((state IN ('proxy_pending','loaded','proxy_failed'))=(successor_proxy_operation_id IS NOT NULL))
);
CREATE UNIQUE INDEX hosting_storage_route_continuity_exclusive ON hosting_storage_route_continuities(proxy_id) WHERE state IN ('reserved','unresolved');

CREATE FUNCTION nerocd_hosting_storage_continuity_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'storage continuity history is immutable'; END IF;
 IF (NEW.operation_id,NEW.proxy_id,NEW.descriptor,NEW.descriptor_hash,NEW.accepted_request,NEW.created_at) IS DISTINCT FROM (OLD.operation_id,OLD.proxy_id,OLD.descriptor,OLD.descriptor_hash,OLD.accepted_request,OLD.created_at) THEN RAISE EXCEPTION 'storage continuity identity is immutable'; END IF;
 IF OLD.state IN ('loaded','proxy_failed','released') AND NEW IS DISTINCT FROM OLD THEN RAISE EXCEPTION 'terminal storage continuity is immutable'; END IF;
 IF OLD.successor_proxy_operation_id IS NOT NULL AND NEW.successor_proxy_operation_id IS DISTINCT FROM OLD.successor_proxy_operation_id THEN RAISE EXCEPTION 'storage continuity successor is immutable'; END IF;
 RETURN NEW;
END;$$;
CREATE TRIGGER hosting_storage_route_continuity_immutable BEFORE UPDATE OR DELETE ON hosting_storage_route_continuities FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_storage_continuity_immutable();

CREATE FUNCTION nerocd_hosting_storage_continuity_consistent() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE op hosting_operations; snap jsonb;
BEGIN
 SELECT * INTO STRICT op FROM hosting_operations WHERE id=NEW.operation_id;
 SELECT body INTO STRICT snap FROM hosting_snapshots WHERE operation_id=NEW.operation_id;
 IF NEW.descriptor->>'operation_id' IS DISTINCT FROM op.id OR NEW.descriptor->>'request_hash' IS DISTINCT FROM op.request_hash OR NEW.descriptor->>'resource_id' IS DISTINCT FROM op.resource_id OR NEW.descriptor->>'binding_id' IS DISTINCT FROM op.binding_id OR NEW.descriptor->>'project_id' IS DISTINCT FROM op.project_id OR NEW.descriptor->>'proxy_id' IS DISTINCT FROM NEW.proxy_id OR NEW.descriptor->'request' IS DISTINCT FROM NEW.accepted_request->'storage_route_continuity' OR snap->'storage_route_continuity'->>'hash' IS DISTINCT FROM NEW.descriptor_hash OR NOT (snap ? 'storage') THEN RAISE EXCEPTION 'storage continuity differs from accepted request'; END IF;
 RETURN NULL;
END;$$;
CREATE CONSTRAINT TRIGGER hosting_storage_route_continuity_consistency AFTER INSERT ON hosting_storage_route_continuities DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_storage_continuity_consistent();
CREATE FUNCTION nerocd_hosting_storage_route_continuity_schema_compatible() RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT to_regclass(current_schema()||'.hosting_storage_route_continuities') IS NOT NULL
 AND EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid=to_regclass(current_schema()||'.hosting_storage_route_continuities') AND contype='p' AND convalidated)
 AND (SELECT count(*) FROM pg_constraint WHERE conrelid=to_regclass(current_schema()||'.hosting_storage_route_continuities') AND contype='f' AND confdeltype='r' AND convalidated)=3
 AND (SELECT count(*) FROM pg_constraint WHERE conrelid=to_regclass(current_schema()||'.hosting_storage_route_continuities') AND contype='c' AND convalidated)=5
 AND EXISTS(SELECT 1 FROM pg_index WHERE indexrelid=to_regclass(current_schema()||'.hosting_storage_route_continuity_exclusive') AND indisunique AND indisvalid AND indpred IS NOT NULL)
 AND EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass(current_schema()||'.hosting_storage_route_continuities') AND tgname='hosting_storage_route_continuity_immutable' AND tgenabled IN ('O','A'))
 AND EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass(current_schema()||'.hosting_storage_route_continuities') AND tgname='hosting_storage_route_continuity_consistency' AND tgdeferrable AND tginitdeferred AND tgenabled IN ('O','A'));
$$;
DO $$ BEGIN
 EXECUTE format('ALTER FUNCTION %I.nerocd_hosting_storage_continuity_immutable() SET search_path=%I,pg_catalog,pg_temp',current_schema(),current_schema());
 EXECUTE format('ALTER FUNCTION %I.nerocd_hosting_storage_continuity_consistent() SET search_path=%I,pg_catalog,pg_temp',current_schema(),current_schema());
 EXECUTE format('ALTER FUNCTION %I.nerocd_hosting_storage_route_continuity_schema_compatible() SET search_path=%I,pg_catalog,pg_temp',current_schema(),current_schema());
END;$$;
