CREATE TABLE services (
 id TEXT PRIMARY KEY, project_id TEXT NOT NULL REFERENCES projects(id), name TEXT NOT NULL,
 repository_id TEXT NOT NULL REFERENCES repositories(id), compose_path TEXT NOT NULL,
 profiles TEXT[] NOT NULL DEFAULT '{}', owner_id TEXT NOT NULL REFERENCES users(id), created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(project_id,name), CHECK (name<>''), CHECK (compose_path<>'')
);
CREATE TABLE environments (
 id TEXT PRIMARY KEY, service_id TEXT NOT NULL REFERENCES services(id), name TEXT NOT NULL,
 runner_selector TEXT[] NOT NULL DEFAULT '{}', compose_project TEXT NOT NULL, health_policy JSONB NOT NULL DEFAULT '{}', confirmation_required BOOLEAN NOT NULL DEFAULT false,
 timeout_seconds INTEGER NOT NULL, secret_bindings JSONB NOT NULL DEFAULT '[]', rollback_safe BOOLEAN NOT NULL DEFAULT true,
 current_healthy_revision_id TEXT, created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(service_id,name), UNIQUE(compose_project), CHECK(name<>''), CHECK(compose_project<>''), CHECK(timeout_seconds>0)
);
CREATE TABLE revisions (
 id TEXT PRIMARY KEY, service_id TEXT NOT NULL REFERENCES services(id), requested_ref TEXT NOT NULL, git_commit TEXT NOT NULL,
 compose_hash TEXT NOT NULL, image_digests TEXT[] NOT NULL DEFAULT '{}', content_identity TEXT NOT NULL,
 created_by TEXT NOT NULL REFERENCES users(id), created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(service_id,content_identity), CHECK(requested_ref<>''), CHECK(git_commit<>''), CHECK(compose_hash<>''), CHECK(content_identity<>'')
);
ALTER TABLE environments ADD CONSTRAINT environments_healthy_revision_fk FOREIGN KEY(current_healthy_revision_id) REFERENCES revisions(id);
CREATE TABLE deployments (
 id TEXT PRIMARY KEY, environment_id TEXT NOT NULL REFERENCES environments(id), desired_revision_id TEXT NOT NULL REFERENCES revisions(id),
 previous_healthy_revision_id TEXT REFERENCES revisions(id), task_run_id TEXT REFERENCES task_runs(id), idempotency_key TEXT NOT NULL,
 status TEXT NOT NULL, requested_by TEXT NOT NULL REFERENCES users(id), confirmed_by TEXT REFERENCES users(id),
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(), updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(), finished_at TIMESTAMPTZ,
 health_passed BOOLEAN, rollback_of_id TEXT REFERENCES deployments(id), failure_code TEXT NOT NULL DEFAULT '', fence_required BOOLEAN NOT NULL DEFAULT true,
 UNIQUE(environment_id,idempotency_key),
 CHECK(status IN ('queued','waiting_confirmation','assigned','preparing','applying','verifying','succeeded','failed','canceled','cancel_requested','rolling_back','rolled_back','rollback_failed','manual_intervention')),
 CHECK(NOT(status='succeeded') OR health_passed IS TRUE), CHECK(NOT(status='succeeded') OR fence_required)
);
CREATE UNIQUE INDEX deployments_one_active_environment ON deployments(environment_id) WHERE status IN ('queued','waiting_confirmation','assigned','preparing','applying','verifying','cancel_requested','rolling_back');
CREATE INDEX deployments_environment_created ON deployments(environment_id,created_at DESC);
CREATE OR REPLACE FUNCTION nerocd_validate_deployment_relationships() RETURNS trigger LANGUAGE plpgsql AS $function$
DECLARE environment_service TEXT; revision_service TEXT;
BEGIN
  -- The row lock is deliberate: it snapshots the healthy pointer and serializes
  -- requests for one environment before the partial active-lock index decides.
  SELECT service_id, current_healthy_revision_id INTO environment_service, NEW.previous_healthy_revision_id FROM environments WHERE id=NEW.environment_id FOR UPDATE;
  IF NOT FOUND THEN RAISE EXCEPTION 'environment does not exist'; END IF;
  SELECT service_id INTO revision_service FROM revisions WHERE id=NEW.desired_revision_id;
  IF revision_service IS DISTINCT FROM environment_service THEN RAISE EXCEPTION 'revision does not belong to environment service'; END IF;
  IF NEW.rollback_of_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM deployments d WHERE d.id=NEW.rollback_of_id AND d.environment_id=NEW.environment_id AND d.previous_healthy_revision_id IS NOT NULL) THEN RAISE EXCEPTION 'rollback deployment must reference same environment with prior healthy revision'; END IF;
  RETURN NEW;
END;
$function$;
CREATE TRIGGER deployments_validate_relationships BEFORE INSERT ON deployments FOR EACH ROW EXECUTE FUNCTION nerocd_validate_deployment_relationships();
CREATE OR REPLACE FUNCTION nerocd_deployment_schema_compatible() RETURNS boolean LANGUAGE sql STABLE AS $function$
 SELECT to_regclass(current_schema() || '.deployments') IS NOT NULL AND to_regclass(current_schema() || '.services') IS NOT NULL;
$function$;
