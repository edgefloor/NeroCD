-- Deployment state is authoritative only through a leased generic run.  Keep
-- attempts separate from run_leases: a run can be re-leased, while the
-- deployment history must retain every fence generation.
ALTER TABLE deployments
    ALTER COLUMN task_run_id SET NOT NULL;

CREATE UNIQUE INDEX deployments_task_run_unique ON deployments(task_run_id);

CREATE TABLE deployment_attempts (
    deployment_id TEXT NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
    run_id TEXT NOT NULL REFERENCES task_runs(id) ON DELETE CASCADE,
    lease_id TEXT NOT NULL REFERENCES run_leases(id) ON DELETE CASCADE,
    runner_id TEXT NOT NULL REFERENCES runners(id),
    attempt INTEGER NOT NULL CHECK (attempt > 0),
    fence TEXT NOT NULL CHECK (fence <> ''),
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    finished_at TIMESTAMPTZ,
    PRIMARY KEY (deployment_id, attempt),
    UNIQUE (lease_id),
    UNIQUE (deployment_id, run_id, lease_id),
    CHECK (run_id <> '')
);

CREATE TABLE deployment_transitions (
    deployment_id TEXT NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
    attempt INTEGER NOT NULL,
    transition_key TEXT NOT NULL CHECK (transition_key <> ''),
    expected_status TEXT NOT NULL,
    target_status TEXT NOT NULL,
    health_passed BOOLEAN,
    failure_code TEXT NOT NULL DEFAULT '',
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (deployment_id, transition_key),
    FOREIGN KEY (deployment_id, attempt) REFERENCES deployment_attempts(deployment_id, attempt) ON DELETE CASCADE
);

CREATE INDEX deployment_attempts_run ON deployment_attempts(run_id, attempt DESC);

CREATE OR REPLACE FUNCTION nerocd_validate_deployment_relationships() RETURNS trigger LANGUAGE plpgsql AS $function$
DECLARE environment_service TEXT; revision_service TEXT; rollback_desired TEXT;
BEGIN
  SELECT service_id, current_healthy_revision_id INTO environment_service, NEW.previous_healthy_revision_id FROM environments WHERE id=NEW.environment_id FOR UPDATE;
  IF NOT FOUND THEN RAISE EXCEPTION 'environment does not exist'; END IF;
  SELECT service_id INTO revision_service FROM revisions WHERE id=NEW.desired_revision_id;
  IF revision_service IS DISTINCT FROM environment_service THEN RAISE EXCEPTION 'revision does not belong to environment service'; END IF;
  IF NEW.rollback_of_id IS NOT NULL THEN
    SELECT previous_healthy_revision_id INTO rollback_desired FROM deployments WHERE id=NEW.rollback_of_id AND environment_id=NEW.environment_id FOR UPDATE;
    IF rollback_desired IS NULL OR NEW.desired_revision_id IS DISTINCT FROM rollback_desired THEN
      RAISE EXCEPTION 'rollback deployment must target the failed deployment previous healthy revision';
    END IF;
  END IF;
  RETURN NEW;
END;
$function$;

CREATE OR REPLACE FUNCTION nerocd_deployment_schema_compatible() RETURNS boolean LANGUAGE sql STABLE AS $function$
 SELECT to_regclass(current_schema() || '.deployments') IS NOT NULL
    AND to_regclass(current_schema() || '.services') IS NOT NULL
    AND to_regclass(current_schema() || '.deployment_attempts') IS NOT NULL
    AND to_regclass(current_schema() || '.deployment_transitions') IS NOT NULL;
$function$;
