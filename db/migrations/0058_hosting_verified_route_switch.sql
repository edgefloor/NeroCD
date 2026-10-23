-- Strict switch predecessor references survive lost responses without protecting
-- historical containers forever. Runtime protection is selected by operation state.
ALTER TABLE hosting_proxy_release_references ADD CONSTRAINT hosting_proxy_release_reference_identity UNIQUE(operation_id,route_id,release_operation_id);
CREATE TABLE hosting_proxy_switch_prior_references (
 operation_id text NOT NULL REFERENCES hosting_proxy_operations(id) ON DELETE RESTRICT,
 route_id text NOT NULL REFERENCES hosting_proxy_routes(id) ON DELETE RESTRICT,
 prior_proxy_operation_id text NOT NULL,
 prior_release_operation_id text NOT NULL,
 prior_version bigint NOT NULL CHECK(prior_version>0),
 PRIMARY KEY(operation_id,route_id),
 FOREIGN KEY(prior_proxy_operation_id,route_id,prior_release_operation_id)
 REFERENCES hosting_proxy_release_references(operation_id,route_id,release_operation_id) ON DELETE RESTRICT
);
CREATE INDEX hosting_proxy_switch_prior_release ON hosting_proxy_switch_prior_references(prior_release_operation_id);
CREATE TRIGGER hosting_proxy_switch_prior_references_immutable BEFORE UPDATE OR DELETE ON hosting_proxy_switch_prior_references FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_execution_immutable();

CREATE FUNCTION nerocd_hosting_verified_switch_consistent() RETURNS trigger LANGUAGE plpgsql AS $function$
DECLARE
 operation text;
 desired hosting_proxy_operations%ROWTYPE;
 prior hosting_proxy_operations%ROWTYPE;
 descriptor jsonb;
 selected jsonb;
 candidate jsonb;
 original jsonb;
 count_selected integer;
BEGIN
 IF TG_TABLE_NAME='hosting_proxy_operations' THEN operation:=NEW.id; ELSE operation:=NEW.operation_id; END IF;
 SELECT * INTO STRICT desired FROM hosting_proxy_operations WHERE id=operation;
 descriptor:=desired.snapshot->'verified_switch';
 IF descriptor IS NULL THEN
  IF EXISTS(SELECT 1 FROM hosting_proxy_switch_prior_references WHERE operation_id=operation) THEN RAISE EXCEPTION 'legacy proxy operation has strict predecessor references'; END IF;
  RETURN NULL;
 END IF;
 IF jsonb_typeof(descriptor) IS DISTINCT FROM 'object' OR descriptor->>'version' IS DISTINCT FROM '1'
 OR jsonb_typeof(descriptor->'routes') IS DISTINCT FROM 'array'
 OR COALESCE((descriptor->>'expected_loaded_generation')::bigint,0)<1 THEN RAISE EXCEPTION 'invalid verified switch descriptor'; END IF;
 count_selected:=jsonb_array_length(descriptor->'routes');
 IF jsonb_array_length(desired.snapshot->'routes')<>(SELECT count(DISTINCT r->>'route_id') FROM jsonb_array_elements(desired.snapshot->'routes') r) OR count_selected<1 OR count_selected>32 OR count_selected<>(SELECT count(DISTINCT r->>'route_id') FROM jsonb_array_elements(descriptor->'routes') r)
 OR count_selected<>(SELECT count(*) FROM hosting_proxy_switch_prior_references WHERE operation_id=operation) THEN RAISE EXCEPTION 'verified switch predecessor reference scope differs'; END IF;
 SELECT * INTO STRICT prior FROM hosting_proxy_operations WHERE id=descriptor->>'prior_applied_proxy_operation_id';
 IF prior.proxy_id<>desired.proxy_id OR prior.generation<>(descriptor->>'expected_loaded_generation')::bigint OR prior.generation>=desired.generation OR prior.state<>'applied'
 OR NOT EXISTS(SELECT 1 FROM hosting_proxy_receipts WHERE operation_id=prior.id AND phase='complete' AND body->>'outcome'='applied' AND (body->>'loaded_generation')::bigint=prior.generation)
 OR jsonb_array_length(prior.snapshot->'routes')<>jsonb_array_length(desired.snapshot->'routes') THEN RAISE EXCEPTION 'verified switch requires exact applied predecessor'; END IF;
 FOR selected IN SELECT * FROM jsonb_array_elements(descriptor->'routes') LOOP
  IF (selected->>'prior_version')::bigint<1 OR selected->>'route_id' IS DISTINCT FROM selected->'prior_target'->>'route_id'
  OR NOT EXISTS(SELECT 1 FROM hosting_proxy_switch_prior_references r WHERE r.operation_id=operation AND r.route_id=selected->>'route_id' AND r.prior_proxy_operation_id=prior.id AND r.prior_release_operation_id=selected->'prior_target'->>'release_operation_id' AND r.prior_version=(selected->>'prior_version')::bigint) THEN RAISE EXCEPTION 'verified switch sidecar differs from descriptor'; END IF;
  SELECT r INTO STRICT original FROM jsonb_array_elements(prior.snapshot->'routes') r WHERE r->>'route_id'=selected->>'route_id';
  IF original IS DISTINCT FROM selected->'prior_target' THEN RAISE EXCEPTION 'verified switch prior target differs'; END IF;
 END LOOP;
 FOR candidate IN SELECT * FROM jsonb_array_elements(desired.snapshot->'routes') LOOP
  SELECT r INTO STRICT original FROM jsonb_array_elements(prior.snapshot->'routes') r WHERE r->>'route_id'=candidate->>'route_id';
  IF EXISTS(SELECT 1 FROM jsonb_array_elements(descriptor->'routes') s WHERE s->>'route_id'=candidate->>'route_id') THEN
   IF (candidate-'release_operation_id'-'host_port') IS DISTINCT FROM (original-'release_operation_id'-'host_port') THEN RAISE EXCEPTION 'verified switch changes route ownership'; END IF;
  ELSIF candidate IS DISTINCT FROM original THEN RAISE EXCEPTION 'verified switch changes unrelated route'; END IF;
 END LOOP;
 RETURN NULL;
END;
$function$;
CREATE CONSTRAINT TRIGGER hosting_proxy_switch_operation_consistent AFTER INSERT OR UPDATE ON hosting_proxy_operations DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_verified_switch_consistent();
CREATE CONSTRAINT TRIGGER hosting_proxy_switch_prior_consistent AFTER INSERT ON hosting_proxy_switch_prior_references DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_verified_switch_consistent();

CREATE FUNCTION nerocd_hosting_verified_route_switch_schema_compatible() RETURNS boolean LANGUAGE sql STABLE AS $function$
 SELECT to_regclass(current_schema()||'.hosting_proxy_switch_prior_references') IS NOT NULL
 AND EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid=to_regclass(current_schema()||'.hosting_proxy_switch_prior_references') AND contype='p' AND convalidated)
 AND (SELECT count(*) FROM pg_constraint WHERE conrelid=to_regclass(current_schema()||'.hosting_proxy_switch_prior_references') AND contype='f' AND confdeltype='r' AND convalidated)=3
 AND EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid=to_regclass(current_schema()||'.hosting_proxy_switch_prior_references') AND contype='c' AND convalidated)
 AND EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid=to_regclass(current_schema()||'.hosting_proxy_release_references') AND conname='hosting_proxy_release_reference_identity' AND contype='u' AND convalidated)
 AND EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass(current_schema()||'.hosting_proxy_switch_prior_references') AND tgname='hosting_proxy_switch_prior_references_immutable' AND tgenabled='O')
 AND EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass(current_schema()||'.hosting_proxy_operations') AND tgname='hosting_proxy_switch_operation_consistent' AND tgdeferrable AND tginitdeferred AND tgenabled='O')
 AND EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass(current_schema()||'.hosting_proxy_switch_prior_references') AND tgname='hosting_proxy_switch_prior_consistent' AND tgdeferrable AND tginitdeferred AND tgenabled='O');
$function$;
DO $migration$
BEGIN
 EXECUTE format('ALTER FUNCTION %I.nerocd_hosting_verified_switch_consistent() SET search_path TO %I, pg_catalog, pg_temp',current_schema(),current_schema());
 EXECUTE format('ALTER FUNCTION %I.nerocd_hosting_verified_route_switch_schema_compatible() SET search_path TO %I, pg_catalog, pg_temp',current_schema(),current_schema());
END;$migration$;
