-- A post-apply failure retains its root environment mutation lock while its
-- single linked rollback child executes. Children are excluded from the root
-- active index, and the unique source link prevents duplicate recovery work.
DROP INDEX deployments_one_active_environment;
CREATE UNIQUE INDEX deployments_one_active_root_environment ON deployments(environment_id)
 WHERE rollback_of_id IS NULL AND status IN ('queued','waiting_confirmation','assigned','preparing','applying','verifying','cancel_requested','rolling_back');
CREATE UNIQUE INDEX deployments_one_rollback_child ON deployments(rollback_of_id) WHERE rollback_of_id IS NOT NULL;

CREATE OR REPLACE FUNCTION nerocd_validate_deployment_relationships() RETURNS trigger LANGUAGE plpgsql AS $function$
DECLARE environment_service TEXT; revision_service TEXT; rollback_desired TEXT; rollback_status TEXT; rollback_reached_apply BOOLEAN;
BEGIN
  SELECT service_id, current_healthy_revision_id INTO environment_service, NEW.previous_healthy_revision_id FROM environments WHERE id=NEW.environment_id FOR UPDATE;
  IF NOT FOUND THEN RAISE EXCEPTION 'environment does not exist'; END IF;
  SELECT service_id INTO revision_service FROM revisions WHERE id=NEW.desired_revision_id;
  IF revision_service IS DISTINCT FROM environment_service THEN RAISE EXCEPTION 'revision does not belong to environment service'; END IF;
  IF NEW.rollback_of_id IS NOT NULL THEN
    SELECT d.previous_healthy_revision_id, d.status, EXISTS (SELECT 1 FROM deployment_transitions t WHERE t.deployment_id=d.id AND t.target_status IN ('applying','verifying'))
      INTO rollback_desired, rollback_status, rollback_reached_apply FROM deployments d WHERE d.id=NEW.rollback_of_id AND d.environment_id=NEW.environment_id FOR UPDATE;
    IF NOT FOUND OR rollback_status <> 'rolling_back' OR NOT rollback_reached_apply OR rollback_desired IS NULL OR NEW.desired_revision_id IS DISTINCT FROM rollback_desired THEN
      RAISE EXCEPTION 'rollback child requires its same-environment post-apply source to be rolling_back and target its previous healthy revision';
    END IF;
  END IF;
  RETURN NEW;
END;
$function$;
