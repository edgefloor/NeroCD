-- A fresh no-clobber download proves replacement at the original CAS location,
-- not physical deletion of the historical inode (which may have been moved).
ALTER TABLE hosting_database_backup_copies ADD COLUMN superseded_by_copy_id text
 REFERENCES hosting_database_backup_copies(id) DEFERRABLE INITIALLY DEFERRED;
DROP INDEX hosting_database_backup_copy_current_location;
CREATE UNIQUE INDEX hosting_database_backup_copy_current_location
 ON hosting_database_backup_copies(backup_id,location,location_hash)
 WHERE availability<>'deleted' AND superseded_by_copy_id IS NULL;

CREATE FUNCTION nerocd_backup_copy_supersession_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='INSERT' THEN
  IF NEW.superseded_by_copy_id IS NOT NULL THEN RAISE EXCEPTION 'copy supersession requires historical generation' USING ERRCODE='23514'; END IF;
  RETURN NEW;
 END IF;
 IF OLD.superseded_by_copy_id IS NOT NULL AND (NEW.superseded_by_copy_id IS DISTINCT FROM OLD.superseded_by_copy_id OR NEW.availability IS DISTINCT FROM OLD.availability) THEN
  RAISE EXCEPTION 'copy supersession is immutable' USING ERRCODE='23514';
 END IF;
 IF NEW.superseded_by_copy_id IS DISTINCT FROM OLD.superseded_by_copy_id THEN
  IF OLD.location<>'local' OR OLD.availability NOT IN ('available','unproven') OR NEW.availability<>OLD.availability OR NEW.superseded_by_copy_id NOT LIKE 'dbc_download_%'
   OR EXISTS(SELECT 1 FROM hosting_database_backup_retention_items i WHERE i.location_hash=OLD.location_hash AND i.reservation_held)
  THEN RAISE EXCEPTION 'copy generation is protected' USING ERRCODE='23514'; END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER hosting_database_backup_copy_supersession_guard BEFORE INSERT OR UPDATE ON hosting_database_backup_copies FOR EACH ROW EXECUTE FUNCTION nerocd_backup_copy_supersession_guard();

CREATE FUNCTION nerocd_backup_copy_supersession_receipt() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.superseded_by_copy_id IS NULL OR NEW.superseded_by_copy_id IS NOT DISTINCT FROM OLD.superseded_by_copy_id THEN RETURN NEW; END IF;
 IF NOT EXISTS(
  SELECT 1 FROM hosting_database_backup_copies n
  JOIN hosting_database_backup_transfers t ON n.id='dbc_download_'||t.id
  JOIN hosting_database_backup_transfer_receipts r ON r.operation_id=t.id AND r.phase='complete'
  WHERE n.id=NEW.superseded_by_copy_id AND n.backup_id=NEW.backup_id AND n.resource_id=NEW.resource_id AND n.runner_id=NEW.runner_id
   AND n.location='local' AND n.location_hash=NEW.location_hash AND n.availability='available' AND n.superseded_by_copy_id IS NULL
   AND t.mode='download' AND t.state='succeeded' AND t.backup_id=n.backup_id AND t.runner_id=n.runner_id
   AND n.body->'local'->>'created'='true' AND n.body->'local'=r.body->'local_copy'
   AND n.body->'item'->>'digest'=NEW.body->'item'->>'digest' AND n.body->'item'->>'bytes'=NEW.body->'item'->>'bytes'
   AND n.body->'local'->>'bytes'=n.body->'item'->>'bytes'
   AND n.body->'local'->>'pool_id'=NEW.body->'local'->>'pool_id'
   AND n.body->'local'->>'device'=NEW.body->'local'->>'device'
   AND n.body->'local'->>'inode'<>NEW.body->'local'->>'inode'
   AND n.body->'local'->>'bytes'=NEW.body->'local'->>'bytes'
   AND n.body->>'server_id'=NEW.body->>'server_id' AND n.body->>'server_id'=t.plan->>'server_id'
   AND n.body->>'destination_id'=NEW.body->>'destination_id' AND n.body->>'destination_id'=t.destination_id
   AND n.body->>'daemon_id'=NEW.body->>'daemon_id' AND n.body->>'daemon_id'=t.daemon_id
 ) THEN RAISE EXCEPTION 'copy supersession lacks exact download receipt' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE CONSTRAINT TRIGGER hosting_database_backup_copy_supersession_receipt AFTER UPDATE ON hosting_database_backup_copies DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION nerocd_backup_copy_supersession_receipt();
