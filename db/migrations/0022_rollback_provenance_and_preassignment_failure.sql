-- A separately-recorded rollback is only meaningful after a deployment has
-- failed *after* it began applying. Cancellation rollback remains in the
-- original deployment (cancel_requested -> rolling_back), so it never races a
-- second active deployment record for the same environment.
CREATE OR REPLACE FUNCTION nerocd_validate_deployment_relationships() RETURNS trigger LANGUAGE plpgsql AS $function$
DECLARE environment_service TEXT; revision_service TEXT; rollback_desired TEXT; rollback_status TEXT; rollback_reached_apply BOOLEAN;
BEGIN
  SELECT service_id, current_healthy_revision_id INTO environment_service, NEW.previous_healthy_revision_id FROM environments WHERE id=NEW.environment_id FOR UPDATE;
  IF NOT FOUND THEN RAISE EXCEPTION 'environment does not exist'; END IF;
  SELECT service_id INTO revision_service FROM revisions WHERE id=NEW.desired_revision_id;
  IF revision_service IS DISTINCT FROM environment_service THEN RAISE EXCEPTION 'revision does not belong to environment service'; END IF;
  IF NEW.rollback_of_id IS NOT NULL THEN
    SELECT d.previous_healthy_revision_id, d.status,
      EXISTS (SELECT 1 FROM deployment_transitions t WHERE t.deployment_id=d.id AND t.target_status IN ('applying','verifying'))
      INTO rollback_desired, rollback_status, rollback_reached_apply
    FROM deployments d WHERE d.id=NEW.rollback_of_id AND d.environment_id=NEW.environment_id FOR UPDATE;
    IF NOT FOUND OR rollback_status <> 'failed' OR NOT rollback_reached_apply
      OR rollback_desired IS NULL OR NEW.desired_revision_id IS DISTINCT FROM rollback_desired THEN
      RAISE EXCEPTION 'rollback deployment must target the same-environment failed post-apply deployment previous healthy revision';
    END IF;
  END IF;
  RETURN NEW;
END;
$function$;

CREATE OR REPLACE FUNCTION nerocd_deployment_schema_compatible() RETURNS boolean LANGUAGE sql STABLE AS $function$
 SELECT to_regclass(current_schema() || '.deployments') IS NOT NULL
    AND to_regclass(current_schema() || '.services') IS NOT NULL
    AND to_regclass(current_schema() || '.deployment_attempts') IS NOT NULL
    AND to_regclass(current_schema() || '.deployment_transitions') IS NOT NULL
    AND EXISTS (
      SELECT 1 FROM information_schema.columns
      WHERE table_schema=current_schema() AND table_name='deployment_attempts'
        AND column_name='status' AND is_nullable='NO'
    );
$function$;
