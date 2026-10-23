-- Operator review and settlement are separate from immutable engine evidence.
CREATE TABLE hosting_proxy_recovery_plans (
 id text PRIMARY KEY,
 operation_id text NOT NULL REFERENCES hosting_proxy_operations(id) ON DELETE RESTRICT,
 actor_id text NOT NULL,
 request_key text NOT NULL CHECK(length(request_key) BETWEEN 1 AND 128),
 request_hash text NOT NULL CHECK(request_hash ~ '^sha256:[0-9a-f]{64}$'),
 body jsonb NOT NULL CHECK(COALESCE(jsonb_typeof(body)='object' AND body->'review'->>'version'='1' AND jsonb_typeof(body->'review'->'routes')='array' AND jsonb_array_length(body->'review'->'routes') BETWEEN 1 AND 32 AND octet_length(body::text)<=131072,false)),
 body_hash text NOT NULL CHECK(body_hash ~ '^sha256:[0-9a-f]{64}$'),
 created_at timestamptz NOT NULL,
 expires_at timestamptz NOT NULL CHECK(expires_at=created_at+interval '30 seconds'),
 CHECK(body->>'id' IS NOT DISTINCT FROM id AND body->'review'->>'operation_id' IS NOT DISTINCT FROM operation_id AND body->'review'->>'actor_id' IS NOT DISTINCT FROM actor_id AND body->>'plan_hash' IS NOT DISTINCT FROM body_hash),
 UNIQUE(operation_id,request_key),
 UNIQUE(id,operation_id,actor_id,body_hash)
);
CREATE INDEX hosting_proxy_recovery_plan_expiry ON hosting_proxy_recovery_plans(operation_id,expires_at);
CREATE TRIGGER hosting_proxy_recovery_plans_immutable BEFORE UPDATE OR DELETE ON hosting_proxy_recovery_plans FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_execution_immutable();

CREATE TABLE hosting_proxy_recovery_resolutions (
 id text PRIMARY KEY,
 operation_id text NOT NULL UNIQUE REFERENCES hosting_proxy_operations(id) ON DELETE RESTRICT,
 plan_id text NOT NULL UNIQUE,
 actor_id text NOT NULL,
 plan_hash text NOT NULL,
 request_key text NOT NULL CHECK(length(request_key) BETWEEN 1 AND 128),
 request_hash text NOT NULL CHECK(request_hash ~ '^sha256:[0-9a-f]{64}$'),
 complete_receipt_id text NOT NULL REFERENCES hosting_proxy_receipts(receipt_id) ON DELETE RESTRICT,
 complete_receipt_hash text NOT NULL CHECK(complete_receipt_hash ~ '^sha256:[0-9a-f]{64}$'),
 convergence_operation_id text NOT NULL UNIQUE REFERENCES hosting_proxy_operations(id) ON DELETE RESTRICT,
 body jsonb NOT NULL CHECK(jsonb_typeof(body)='object' AND octet_length(body::text)<=131072),
 body_hash text NOT NULL CHECK(body_hash ~ '^sha256:[0-9a-f]{64}$'),
 created_at timestamptz NOT NULL,
 FOREIGN KEY(plan_id,operation_id,actor_id,plan_hash) REFERENCES hosting_proxy_recovery_plans(id,operation_id,actor_id,body_hash) ON DELETE RESTRICT
);
CREATE TRIGGER hosting_proxy_recovery_resolutions_immutable BEFORE UPDATE OR DELETE ON hosting_proxy_recovery_resolutions FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_execution_immutable();

CREATE FUNCTION nerocd_hosting_proxy_recovery_consistent() RETURNS trigger LANGUAGE plpgsql AS $function$
DECLARE p hosting_proxy_recovery_plans%ROWTYPE; o hosting_proxy_operations%ROWTYPE; c hosting_proxy_operations%ROWTYPE; r hosting_proxy_receipts%ROWTYPE; selected jsonb; change jsonb; repaired hosting_proxy_routes%ROWTYPE;
BEGIN
 SELECT * INTO STRICT p FROM hosting_proxy_recovery_plans WHERE id=NEW.plan_id;
 SELECT * INTO STRICT o FROM hosting_proxy_operations WHERE id=NEW.operation_id;
 SELECT * INTO STRICT c FROM hosting_proxy_operations WHERE id=NEW.convergence_operation_id;
 SELECT * INTO STRICT r FROM hosting_proxy_receipts WHERE receipt_id=NEW.complete_receipt_id;
 IF p.body->'review'->>'operation_id' IS DISTINCT FROM o.id
 OR p.body->'review'->>'actor_id' IS DISTINCT FROM NEW.actor_id
 OR p.body->'review'->>'complete_receipt_id' IS DISTINCT FROM r.receipt_id
 OR p.body->'review'->>'complete_receipt_hash' IS DISTINCT FROM r.body_hash
 OR r.operation_id<>o.id OR r.phase<>'complete' OR r.body_hash<>NEW.complete_receipt_hash
 OR r.body->>'outcome' NOT IN ('rolled_back','refused') OR o.state IS DISTINCT FROM r.body->>'outcome'
 OR o.snapshot->'verified_switch' IS NULL OR c.proxy_id<>o.proxy_id
 OR c.snapshot->'verified_switch' IS NOT NULL OR c.generation<=o.generation
 OR c.created_by<>NEW.actor_id OR NEW.created_at>=p.expires_at
 OR NEW.body->>'id' IS DISTINCT FROM NEW.id OR NEW.body->>'operation_id' IS DISTINCT FROM o.id
 OR NEW.body->>'plan_id' IS DISTINCT FROM p.id OR NEW.body->>'plan_hash' IS DISTINCT FROM p.body_hash
 OR NEW.body->>'actor_id' IS DISTINCT FROM NEW.actor_id OR NEW.body->>'engine_outcome' IS DISTINCT FROM r.body->>'outcome'
 OR NEW.body->>'complete_receipt_id' IS DISTINCT FROM r.receipt_id OR NEW.body->>'complete_receipt_hash' IS DISTINCT FROM r.body_hash
 OR NEW.body->>'convergence_operation_id' IS DISTINCT FROM c.id
 OR (SELECT COALESCE(jsonb_agg(x ORDER BY x->>'route_id'),'[]'::jsonb) FROM jsonb_array_elements(c.snapshot->'routes') x) IS DISTINCT FROM p.body->'review'->'convergence_routes'
 OR jsonb_array_length(NEW.body->'routes') IS DISTINCT FROM jsonb_array_length(p.body->'review'->'routes')
 OR EXISTS(SELECT 1 FROM hosting_proxies WHERE id=o.proxy_id AND (hold_operation_id=o.id OR active_operation_id=o.id))
 THEN RAISE EXCEPTION 'proxy recovery evidence differs'; END IF;
 FOR selected IN SELECT * FROM jsonb_array_elements(p.body->'review'->'routes') LOOP
  SELECT x INTO STRICT change FROM jsonb_array_elements(NEW.body->'routes') x WHERE x->>'route_id'=selected->>'route_id';
  SELECT * INTO STRICT repaired FROM hosting_proxy_routes WHERE id=selected->>'route_id';
  IF change->>'before_version' IS DISTINCT FROM selected->>'version'
   OR change->>'before_release_operation_id' IS DISTINCT FROM selected->>'selected_release_operation_id'
   OR change->>'after_release_operation_id' IS DISTINCT FROM selected->'prior_target'->>'release_operation_id'
   OR (change->>'enabled')::boolean IS DISTINCT FROM (selected->>'enabled')::boolean
   OR (change->>'after_version')::bigint IS DISTINCT FROM ((selected->>'version')::bigint+CASE WHEN selected->>'selected_release_operation_id'=selected->'prior_target'->>'release_operation_id' THEN 0 ELSE 1 END)
   OR repaired.selected_release_operation_id IS DISTINCT FROM change->>'after_release_operation_id'
   OR repaired.version IS DISTINCT FROM (change->>'after_version')::bigint OR repaired.enabled IS DISTINCT FROM (selected->>'enabled')::boolean
  THEN RAISE EXCEPTION 'proxy recovery metadata transition differs'; END IF;
 END LOOP;
 RETURN NULL;
END;$function$;
CREATE CONSTRAINT TRIGGER hosting_proxy_recovery_resolution_consistent AFTER INSERT ON hosting_proxy_recovery_resolutions DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_proxy_recovery_consistent();

CREATE FUNCTION nerocd_hosting_proxy_metadata_recovery_schema_compatible() RETURNS boolean LANGUAGE sql STABLE AS $function$
 SELECT to_regclass(current_schema()||'.hosting_proxy_recovery_plans') IS NOT NULL
 AND to_regclass(current_schema()||'.hosting_proxy_recovery_resolutions') IS NOT NULL
 AND (SELECT count(*) FROM pg_constraint WHERE conrelid=to_regclass(current_schema()||'.hosting_proxy_recovery_resolutions') AND contype='f' AND confdeltype='r' AND convalidated)=4
 AND EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass(current_schema()||'.hosting_proxy_recovery_plans') AND tgname='hosting_proxy_recovery_plans_immutable' AND tgenabled='O')
 AND EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass(current_schema()||'.hosting_proxy_recovery_resolutions') AND tgname='hosting_proxy_recovery_resolutions_immutable' AND tgenabled='O')
 AND EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass(current_schema()||'.hosting_proxy_recovery_resolutions') AND tgname='hosting_proxy_recovery_resolution_consistent' AND tgdeferrable AND tginitdeferred AND tgenabled='O');
$function$;
DO $migration$ BEGIN
 EXECUTE format('ALTER FUNCTION %I.nerocd_hosting_proxy_recovery_consistent() SET search_path TO %I, pg_catalog, pg_temp',current_schema(),current_schema());
 EXECUTE format('ALTER FUNCTION %I.nerocd_hosting_proxy_metadata_recovery_schema_compatible() SET search_path TO %I, pg_catalog, pg_temp',current_schema(),current_schema());
END;$migration$;
