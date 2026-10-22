-- Inert source/build persistence. No River job, resource source switch, or executor is installed.
ALTER TABLE resources ADD CONSTRAINT resources_build_project_key UNIQUE(id,project_id);
ALTER TABLE project_managed_secrets ADD CONSTRAINT managed_secrets_build_project_key UNIQUE(id,project_id);
CREATE TABLE hosting_build_sources (
 id text PRIMARY KEY CHECK(octet_length(id) BETWEEN 1 AND 128),
 resource_id text NOT NULL UNIQUE,
 project_id text NOT NULL,
 generation bigint NOT NULL CHECK(generation>0),
 UNIQUE(id,resource_id,project_id),
 FOREIGN KEY(resource_id,project_id) REFERENCES resources(id,project_id) ON DELETE RESTRICT
);
CREATE TABLE hosting_build_source_versions (
 source_id text NOT NULL REFERENCES hosting_build_sources(id) ON DELETE RESTRICT,
 generation bigint NOT NULL CHECK(generation>0),
 body jsonb NOT NULL CHECK(jsonb_typeof(body)='object' AND octet_length(body::text)<=65536),
 body_hash text NOT NULL CHECK(body_hash~'^sha256:[0-9a-f]{64}$'),
 created_by text NOT NULL REFERENCES principals(id),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(source_id,generation),
 CHECK((body->>'id'=source_id AND (body->>'generation')::bigint=generation) IS TRUE)
);
ALTER TABLE hosting_build_sources ADD CONSTRAINT hosting_build_source_current_fk FOREIGN KEY(id,generation) REFERENCES hosting_build_source_versions(source_id,generation) DEFERRABLE INITIALLY DEFERRED;
CREATE TABLE hosting_source_grants (
 id text PRIMARY KEY,
 version bigint NOT NULL CHECK(version=1),
 source_id text NOT NULL,
 resource_id text NOT NULL,
 project_id text NOT NULL,
 secret_id text NOT NULL,
 secret_version bigint NOT NULL,
 kind text NOT NULL CHECK(kind IN('https_basic','ssh_private_key')),
 repository_url text NOT NULL CHECK(octet_length(repository_url) BETWEEN 1 AND 8192),
 policy_hash text NOT NULL CHECK(policy_hash~'^sha256:[0-9a-f]{64}$'),
 created_by text NOT NULL REFERENCES principals(id),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 revoked_at timestamptz,
 FOREIGN KEY(source_id,resource_id,project_id) REFERENCES hosting_build_sources(id,resource_id,project_id),
 FOREIGN KEY(secret_id,project_id) REFERENCES project_managed_secrets(id,project_id),
 FOREIGN KEY(secret_id,secret_version) REFERENCES project_managed_secret_versions(secret_id,version)
);
CREATE TABLE hosting_builds (
 id text PRIMARY KEY CHECK(octet_length(id) BETWEEN 1 AND 63 AND id~'^[a-z0-9][a-z0-9-]*$'),
 resource_id text NOT NULL,
 project_id text NOT NULL,
 source_id text NOT NULL,
 source_generation bigint NOT NULL,
 idempotency_key text NOT NULL CHECK(octet_length(idempotency_key) BETWEEN 1 AND 128),
 request_hash text NOT NULL CHECK(request_hash~'^sha256:[0-9a-f]{64}$'),
 snapshot jsonb NOT NULL CHECK(jsonb_typeof(snapshot)='object' AND octet_length(snapshot::text)<=131072),
 snapshot_hash text NOT NULL CHECK(snapshot_hash~'^sha256:[0-9a-f]{64}$'),
 state text NOT NULL DEFAULT 'queued' CHECK(state IN('queued','sourcing','building','succeeded','failed','canceled','cleanup_required')),
 revision bigint NOT NULL DEFAULT 1 CHECK(revision>0),
 run_id text UNIQUE REFERENCES task_runs(id),
 daemon_id text NOT NULL DEFAULT '' CHECK(octet_length(daemon_id)<=256),
 failure_code text NOT NULL DEFAULT '' CHECK(failure_code='' OR failure_code~'^[a-z][a-z0-9_]{0,63}$'),
 created_by text NOT NULL REFERENCES principals(id),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(resource_id,idempotency_key),
 FOREIGN KEY(source_id,resource_id,project_id) REFERENCES hosting_build_sources(id,resource_id,project_id),
 FOREIGN KEY(source_id,source_generation) REFERENCES hosting_build_source_versions(source_id,generation),
 CHECK((snapshot->>'build_id'=id AND snapshot->'source'->>'id'=source_id AND (snapshot->'source'->>'generation')::bigint=source_generation) IS TRUE)
);
CREATE TABLE hosting_build_tasks (
 build_id text PRIMARY KEY REFERENCES hosting_builds(id),
 run_id text NOT NULL UNIQUE REFERENCES task_runs(id),
 runner_id text NOT NULL REFERENCES runners(id)
);
CREATE TABLE hosting_build_receipts (
 build_id text NOT NULL REFERENCES hosting_builds(id),
 phase text NOT NULL CHECK(phase IN('source','cleanup','complete')),
 receipt_id text NOT NULL UNIQUE CHECK(octet_length(receipt_id) BETWEEN 1 AND 128),
 authority jsonb NOT NULL CHECK(jsonb_typeof(authority)='object'),
 body jsonb NOT NULL CHECK(jsonb_typeof(body)='object' AND octet_length(body::text)<=32768),
 body_hash text NOT NULL CHECK(body_hash~'^sha256:[0-9a-f]{64}$'),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(build_id,phase)
);
CREATE TABLE hosting_build_artifacts (
 build_id text PRIMARY KEY REFERENCES hosting_builds(id),
 body jsonb NOT NULL CHECK(jsonb_typeof(body)='object' AND octet_length(body::text)<=16384 AND (body->>'build_id'=build_id) IS TRUE),
 body_hash text NOT NULL CHECK(body_hash~'^sha256:[0-9a-f]{64}$'),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX hosting_builds_resource_page_idx ON hosting_builds(resource_id,created_at,id);
CREATE TRIGGER hosting_build_source_versions_immutable BEFORE UPDATE OR DELETE ON hosting_build_source_versions FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_target_immutable();
CREATE TRIGGER hosting_build_tasks_immutable BEFORE UPDATE OR DELETE ON hosting_build_tasks FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_target_immutable();
CREATE TRIGGER hosting_build_receipts_immutable BEFORE UPDATE OR DELETE ON hosting_build_receipts FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_target_immutable();
CREATE TRIGGER hosting_build_artifacts_immutable BEFORE UPDATE OR DELETE ON hosting_build_artifacts FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_target_immutable();
CREATE FUNCTION nerocd_hosting_build_source_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'source history is immutable' USING ERRCODE='23514'; END IF;
 IF (NEW.id,NEW.resource_id,NEW.project_id) IS DISTINCT FROM (OLD.id,OLD.resource_id,OLD.project_id) OR NEW.generation<>OLD.generation+1 THEN RAISE EXCEPTION 'invalid source generation' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END;$$;
CREATE TRIGGER hosting_build_source_identity BEFORE UPDATE OR DELETE ON hosting_build_sources FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_build_source_update();
CREATE FUNCTION nerocd_hosting_source_grant_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'grant history is immutable' USING ERRCODE='23514'; END IF;
 IF (to_jsonb(NEW)-'revoked_at') IS DISTINCT FROM (to_jsonb(OLD)-'revoked_at') OR OLD.revoked_at IS NOT NULL OR NEW.revoked_at IS NULL THEN RAISE EXCEPTION 'grant identity is immutable' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END;$$;
CREATE TRIGGER hosting_source_grant_identity BEFORE UPDATE OR DELETE ON hosting_source_grants FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_source_grant_update();
CREATE FUNCTION nerocd_hosting_build_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'build history is immutable' USING ERRCODE='23514'; END IF;
 IF (to_jsonb(NEW)-ARRAY['state','revision','run_id','daemon_id','failure_code','updated_at']) IS DISTINCT FROM (to_jsonb(OLD)-ARRAY['state','revision','run_id','daemon_id','failure_code','updated_at']) OR NEW.revision<>OLD.revision+1 OR (OLD.run_id IS NOT NULL AND NEW.run_id IS DISTINCT FROM OLD.run_id) OR (OLD.daemon_id<>'' AND NEW.daemon_id<>OLD.daemon_id) THEN RAISE EXCEPTION 'build identity is immutable' USING ERRCODE='23514'; END IF;
 IF OLD.state IN('succeeded','failed','canceled') OR NOT (NEW.state=OLD.state OR (OLD.state='queued' AND NEW.state IN('sourcing','failed','canceled','cleanup_required')) OR (OLD.state='sourcing' AND NEW.state IN('building','failed','canceled','cleanup_required')) OR (OLD.state='building' AND NEW.state IN('succeeded','failed','canceled','cleanup_required')) OR (OLD.state='cleanup_required' AND NEW.state IN('failed','canceled'))) THEN RAISE EXCEPTION 'invalid build transition' USING ERRCODE='23514'; END IF;
 IF NEW.state='building' AND (NEW.daemon_id='' OR NOT EXISTS(SELECT 1 FROM hosting_build_receipts WHERE build_id=NEW.id AND phase='source')) THEN RAISE EXCEPTION 'build preparation requires source and daemon pin' USING ERRCODE='23514'; END IF;
 IF NEW.state='succeeded' AND NOT EXISTS(SELECT 1 FROM hosting_build_artifacts WHERE build_id=NEW.id) THEN RAISE EXCEPTION 'success requires artifact' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END;$$;
CREATE TRIGGER hosting_build_identity BEFORE UPDATE OR DELETE ON hosting_builds FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_build_update();
CREATE FUNCTION nerocd_hosting_build_lease_target() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE spec jsonb; target text; build_state text;
BEGIN
 SELECT run_spec INTO spec FROM task_runs WHERE id=NEW.run_id;
 IF spec->>'type'='hosting_build_v1' OR EXISTS(SELECT 1 FROM hosting_build_tasks WHERE run_id=NEW.run_id) THEN
  SELECT t.runner_id,b.state INTO target,build_state FROM hosting_build_tasks t JOIN hosting_builds b ON b.id=t.build_id WHERE t.run_id=NEW.run_id AND spec=jsonb_build_object('type','hosting_build_v1','hosting_build_id',b.id) AND b.run_id=NEW.run_id FOR SHARE OF b;
  IF NOT FOUND OR target<>NEW.runner_id OR (NEW.status='active' AND build_state NOT IN('queued','sourcing','building','cleanup_required')) THEN RAISE EXCEPTION 'build lease target mismatch' USING ERRCODE='23514'; END IF;
 END IF;
 RETURN NEW;
END;$$;
CREATE TRIGGER hosting_build_lease_admission BEFORE INSERT OR UPDATE ON run_leases FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_build_lease_target();

-- Defense in depth for callers using the narrow transaction seam. These checks
-- bind JSON identities to their relational parent; Go validates semantic hashes.
CREATE FUNCTION nerocd_hosting_build_source_body() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NOT EXISTS(SELECT 1 FROM hosting_build_sources h WHERE h.id=NEW.source_id AND h.resource_id=NEW.body->>'resource_id' AND h.project_id=NEW.body->>'project_id') THEN RAISE EXCEPTION 'source body scope mismatch' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END;$$;
CREATE TRIGGER hosting_build_source_body BEFORE INSERT ON hosting_build_source_versions FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_build_source_body();
CREATE FUNCTION nerocd_hosting_build_artifact_body() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NOT EXISTS(SELECT 1 FROM hosting_builds b JOIN hosting_build_receipts r ON r.build_id=b.id AND r.phase='source' WHERE b.id=NEW.build_id AND b.state='building' AND b.daemon_id<>'' AND NEW.body->>'daemon_id'=b.daemon_id AND NEW.body->>'source_id'=b.source_id AND NEW.body->>'input_id'=b.snapshot->>'input_id' AND NEW.body->>'source_receipt_hash'=r.body_hash AND NEW.body->>'platform'=b.snapshot->'operator_policy'->>'platform' AND NEW.body->>'origin'='local_build') THEN RAISE EXCEPTION 'artifact scope mismatch' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END;$$;
CREATE TRIGGER hosting_build_artifact_body BEFORE INSERT ON hosting_build_artifacts FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_build_artifact_body();
CREATE FUNCTION nerocd_hosting_build_schema_compatible() RETURNS boolean LANGUAGE sql STABLE AS $$
SELECT to_regclass(current_schema()||'.hosting_builds') IS NOT NULL
 AND to_regclass(current_schema()||'.hosting_build_source_versions') IS NOT NULL
 AND to_regclass(current_schema()||'.hosting_source_grants') IS NOT NULL
 AND to_regclass(current_schema()||'.hosting_build_receipts') IS NOT NULL
 AND to_regclass(current_schema()||'.hosting_build_artifacts') IS NOT NULL
 AND EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass(current_schema()||'.run_leases') AND tgname='hosting_build_lease_admission' AND tgenabled IN('O','A'))
 AND EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass(current_schema()||'.hosting_builds') AND tgname='hosting_build_identity' AND tgenabled IN('O','A'))
 AND EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass(current_schema()||'.hosting_build_artifacts') AND tgname='hosting_build_artifact_body' AND tgenabled IN('O','A'));
$$;
