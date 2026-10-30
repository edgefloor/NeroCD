-- External SQL is staged under its own authority; it is never a fabricated backup.
CREATE TABLE hosting_database_import_stages (
 id text PRIMARY KEY,
 project_id text NOT NULL REFERENCES projects(id),
 target_resource_id text NOT NULL REFERENCES resources(id),
 target_binding_id text NOT NULL REFERENCES resource_bindings(id),
 actor_id text NOT NULL REFERENCES principals(id),
 request_id text NOT NULL,
 request_hash text NOT NULL,
 runner_id text NOT NULL REFERENCES runners(id),
 daemon_id text NOT NULL,
 plan jsonb NOT NULL,
 plan_hash text NOT NULL,
 body jsonb NOT NULL,
 ready_receipt jsonb,
 run_id text UNIQUE REFERENCES task_runs(id),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(target_resource_id,request_id),
 CHECK(body->>'id'=id AND body->>'target_resource_id'=target_resource_id),
 CHECK(body->>'version'='mysql.compose-external-s3-stage.v1'),
 CHECK(body->>'state' IN ('queued','running','ready','failed','expired','consumed')),
 CHECK(plan->>'hash'=plan_hash AND plan->>'actor_id'=actor_id AND plan->>'runner_id'=runner_id AND plan->>'project_id'=project_id),
 CHECK(plan->'target'->'release'->>'daemon_id'=daemon_id AND plan->'target'->'snapshot'->>'binding_id'=target_binding_id)
);
CREATE INDEX hosting_database_import_stage_cleanup ON hosting_database_import_stages((body->>'state'),updated_at);

CREATE TABLE hosting_database_imports (
 id text PRIMARY KEY,
 stage_id text NOT NULL UNIQUE REFERENCES hosting_database_import_stages(id),
 project_id text NOT NULL REFERENCES projects(id),
 target_resource_id text NOT NULL REFERENCES resources(id),
 target_binding_id text NOT NULL REFERENCES resource_bindings(id),
 actor_id text NOT NULL REFERENCES principals(id),
 request_id text NOT NULL,
 request_hash text NOT NULL,
 runner_id text NOT NULL REFERENCES runners(id),
 daemon_id text NOT NULL,
 plan jsonb NOT NULL,
 plan_hash text NOT NULL,
 body jsonb NOT NULL,
 ownership_held boolean NOT NULL DEFAULT true,
 run_id text UNIQUE REFERENCES task_runs(id),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(target_resource_id,request_id),
 CHECK(body->>'id'=id AND body->>'stage_id'=stage_id AND body->>'target_resource_id'=target_resource_id),
 CHECK(body->>'version'='mysql.compose-external-import.v1'),
 CHECK(body->>'state' IN ('queued','running','succeeded','failed','uncertain')),
 CHECK((body->>'ownership_held')::boolean=ownership_held),
 CHECK(plan->>'hash'=plan_hash AND plan->'stage_plan'->>'actor_id'=actor_id AND plan->'stage_plan'->>'runner_id'=runner_id),
 CHECK(plan->'stage_plan'->'target'->'release'->>'daemon_id'=daemon_id)
);
CREATE UNIQUE INDEX hosting_database_import_binding_owner ON hosting_database_imports(target_binding_id) WHERE ownership_held;

CREATE TABLE hosting_database_import_receipts (
 operation_id text NOT NULL,
 phase text NOT NULL CHECK(phase IN ('stage_intent','ready','stage_failed','import_intent','complete','failed','uncertain')),
 receipt_id text NOT NULL UNIQUE,
 authority jsonb NOT NULL,
 body jsonb NOT NULL,
 body_hash text NOT NULL,
 response jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(operation_id,phase)
);

-- Every competing effect uses the existing destination lock and stable binding.
-- External import ownership also gates old deployment/backup entry points.
CREATE FUNCTION nerocd_external_import_binding_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE binding_key text; destination_key text;
BEGIN
 IF TG_TABLE_NAME='hosting_database_imports' THEN binding_key:=NEW.target_binding_id;
 ELSIF TG_TABLE_NAME='hosting_operations' THEN binding_key:=NEW.binding_id;
 ELSE binding_key:=CASE WHEN NEW.mode='backup' THEN NEW.source_binding_id ELSE NEW.target_binding_id END;
 END IF;
 SELECT destination_id INTO destination_key FROM resource_bindings WHERE id=binding_key;
 PERFORM pg_advisory_xact_lock(hashtextextended('nerocd-hosting-destination:'||destination_key,0));
 PERFORM 1 FROM hosting_binding_state WHERE binding_id=binding_key FOR UPDATE;
 IF EXISTS(SELECT 1 FROM hosting_database_imports i WHERE i.target_binding_id=binding_key AND i.ownership_held) THEN
  RAISE EXCEPTION 'external database import owns target' USING ERRCODE='23514';
 END IF;
 IF TG_TABLE_NAME='hosting_database_imports' AND (EXISTS(SELECT 1 FROM hosting_database_backups b WHERE b.ownership_held AND (b.source_binding_id=binding_key OR b.target_binding_id=binding_key)) OR EXISTS(SELECT 1 FROM hosting_operations o WHERE o.binding_id=binding_key AND o.state NOT IN ('succeeded','failed','canceled'))) THEN
  RAISE EXCEPTION 'external database import target busy' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
DO $$ BEGIN EXECUTE format('ALTER FUNCTION nerocd_external_import_binding_guard() SET search_path TO pg_catalog,%I',current_schema()); END $$;
CREATE TRIGGER external_import_binding_guard BEFORE INSERT ON hosting_database_imports FOR EACH ROW EXECUTE FUNCTION nerocd_external_import_binding_guard();
CREATE TRIGGER external_import_deployment_guard BEFORE INSERT ON hosting_operations FOR EACH ROW EXECUTE FUNCTION nerocd_external_import_binding_guard();
CREATE TRIGGER external_import_backup_guard BEFORE INSERT ON hosting_database_backups FOR EACH ROW EXECUTE FUNCTION nerocd_external_import_binding_guard();

CREATE FUNCTION nerocd_external_import_lease() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE spec jsonb; expected_runner text; expected_mode text; expected_id text; caps text[];
BEGIN
 SELECT run_spec INTO spec FROM task_runs WHERE id=NEW.run_id;
 IF spec->>'type' NOT IN ('database_import_stage','database_import') THEN RETURN NEW; END IF;
 expected_mode:=CASE WHEN spec->>'type'='database_import_stage' THEN 'stage' ELSE 'import' END;
 IF expected_mode='stage' THEN
  SELECT id,runner_id INTO expected_id,expected_runner FROM hosting_database_import_stages WHERE run_id=NEW.run_id AND body->>'state'='queued';
 ELSE
  SELECT id,runner_id INTO expected_id,expected_runner FROM hosting_database_imports WHERE run_id=NEW.run_id AND ownership_held AND body->>'state'='queued';
 END IF;
 SELECT capabilities INTO caps FROM runners WHERE id=NEW.runner_id AND status='active' AND token_hash<>'' FOR SHARE;
 IF expected_id IS NULL OR expected_runner IS DISTINCT FROM NEW.runner_id OR caps IS NULL OR 'shell'=ANY(caps) OR NOT(caps @> ARRAY[spec->>'type','hosting_managed_compose_v1',CASE WHEN expected_mode='stage' THEN 'hosting_compose_mysql_import_stage_v1' ELSE 'hosting_compose_mysql_import_v1' END]::text[]) OR spec<>jsonb_build_object('type',spec->>'type','hosting_operation_id',expected_id,'hosting_mode',expected_mode,'inputs','{}'::jsonb) OR EXISTS(SELECT 1 FROM run_leases WHERE run_id=NEW.run_id) OR NOT EXISTS(SELECT 1 FROM runner_enrollments e JOIN runners r ON r.id=e.runner_id WHERE e.runner_id=NEW.runner_id AND e.used_at IS NOT NULL AND e.revoked_at IS NULL AND e.credential_hash=r.token_hash) THEN
  RAISE EXCEPTION 'external database import lease mismatch' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
DO $$ BEGIN EXECUTE format('ALTER FUNCTION nerocd_external_import_lease() SET search_path TO pg_catalog,%I',current_schema()); END $$;
CREATE TRIGGER external_import_lease BEFORE INSERT ON run_leases FOR EACH ROW EXECUTE FUNCTION nerocd_external_import_lease();

CREATE TABLE hosting_database_import_deliveries (
 delivery_id text PRIMARY KEY,
 stage_id text NOT NULL REFERENCES hosting_database_import_stages(id),
 metadata jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE FUNCTION nerocd_external_import_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_TABLE_NAME IN ('hosting_database_import_receipts','hosting_database_import_deliveries') THEN
  RAISE EXCEPTION 'immutable external import evidence' USING ERRCODE='23514';
 END IF;
 IF (to_jsonb(NEW)-ARRAY['body','ready_receipt','run_id','ownership_held','updated_at']) IS DISTINCT FROM (to_jsonb(OLD)-ARRAY['body','ready_receipt','run_id','ownership_held','updated_at']) OR (OLD.run_id IS NOT NULL AND NEW.run_id IS DISTINCT FROM OLD.run_id) THEN
  RAISE EXCEPTION 'immutable external import authority' USING ERRCODE='23514';
 END IF;
 IF TG_TABLE_NAME='hosting_database_import_stages' THEN
  IF OLD.ready_receipt IS NOT NULL AND NEW.ready_receipt IS DISTINCT FROM OLD.ready_receipt THEN
   RAISE EXCEPTION 'immutable stage receipt' USING ERRCODE='23514';
  END IF;
 END IF;
 RETURN NEW;
END $$;
DO $$ BEGIN EXECUTE format('ALTER FUNCTION nerocd_external_import_immutable() SET search_path TO pg_catalog,%I',current_schema()); END $$;
CREATE TRIGGER external_import_stage_immutable BEFORE UPDATE ON hosting_database_import_stages FOR EACH ROW EXECUTE FUNCTION nerocd_external_import_immutable();
CREATE TRIGGER external_import_immutable BEFORE UPDATE ON hosting_database_imports FOR EACH ROW EXECUTE FUNCTION nerocd_external_import_immutable();
CREATE TRIGGER external_import_receipt_immutable BEFORE UPDATE OR DELETE ON hosting_database_import_receipts FOR EACH ROW EXECUTE FUNCTION nerocd_external_import_immutable();
CREATE TRIGGER external_import_delivery_immutable BEFORE UPDATE OR DELETE ON hosting_database_import_deliveries FOR EACH ROW EXECUTE FUNCTION nerocd_external_import_immutable();
