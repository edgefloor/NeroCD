ALTER TABLE run_logs ADD COLUMN component text NOT NULL DEFAULT '' CHECK(component IN('','build','buildkit','railpack'));
ALTER TABLE run_logs ADD COLUMN source_timestamp timestamptz;

ALTER TABLE hosting_build_tasks ADD CONSTRAINT hosting_build_tasks_log_identity UNIQUE(build_id,run_id);
CREATE TABLE hosting_build_log_states (
 build_id text PRIMARY KEY REFERENCES hosting_build_tasks(build_id),
 run_id text NOT NULL UNIQUE REFERENCES hosting_build_tasks(run_id),
 last_sequence integer NOT NULL DEFAULT 0 CHECK(last_sequence>=0),
 deleted_through_sequence integer NOT NULL DEFAULT 0 CHECK(deleted_through_sequence>=0 AND deleted_through_sequence<=last_sequence),
 history_complete boolean NOT NULL,
 UNIQUE(build_id,run_id),
 FOREIGN KEY(build_id,run_id) REFERENCES hosting_build_tasks(build_id,run_id)
);
CREATE TABLE hosting_build_log_receipts (
 build_id text NOT NULL REFERENCES hosting_build_log_states(build_id),
 run_id text NOT NULL REFERENCES hosting_build_log_states(run_id),
 event_key text NOT NULL CHECK(octet_length(event_key) BETWEEN 1 AND 128),
 authority jsonb NOT NULL CHECK(jsonb_typeof(authority)='object'),
 requested_sequence integer NOT NULL CHECK(requested_sequence>0),
 sequence integer NOT NULL CHECK(sequence>0),
 body_hash text NOT NULL CHECK(body_hash~'^sha256:[0-9a-f]{64}$'),
 PRIMARY KEY(run_id,event_key), UNIQUE(run_id,sequence),
 FOREIGN KEY(build_id,run_id) REFERENCES hosting_build_log_states(build_id,run_id),
 CHECK((authority->>'build_id'=build_id AND authority->>'run_id'=run_id) IS TRUE)
);
-- Existing rows cannot prove the sequence of previously deleted messages.
INSERT INTO hosting_build_log_states(build_id,run_id,last_sequence,history_complete)
 SELECT t.build_id,t.run_id,COALESCE(MAX(l.sequence),0),false FROM hosting_build_tasks t LEFT JOIN run_logs l ON l.run_id=t.run_id GROUP BY t.build_id,t.run_id;
CREATE FUNCTION nerocd_build_log_dispatch() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 INSERT INTO hosting_build_log_states(build_id,run_id,history_complete) VALUES(NEW.build_id,NEW.run_id,true);
 RETURN NEW;
END $$;
CREATE TRIGGER hosting_build_log_dispatch AFTER INSERT ON hosting_build_tasks FOR EACH ROW EXECUTE FUNCTION nerocd_build_log_dispatch();
CREATE FUNCTION nerocd_build_log_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_TABLE_NAME='hosting_build_log_states' AND TG_OP='UPDATE' THEN
  IF NEW.build_id=OLD.build_id AND NEW.run_id=OLD.run_id AND NEW.history_complete=OLD.history_complete AND NEW.last_sequence>=OLD.last_sequence AND NEW.deleted_through_sequence>=OLD.deleted_through_sequence THEN RETURN NEW; END IF;
 END IF;
 RAISE EXCEPTION 'build log identity and receipts are immutable' USING ERRCODE='23514';
END $$;
CREATE TRIGGER hosting_build_log_state_identity BEFORE UPDATE OR DELETE ON hosting_build_log_states FOR EACH ROW EXECUTE FUNCTION nerocd_build_log_immutable();
CREATE TRIGGER hosting_build_log_receipt_identity BEFORE UPDATE OR DELETE ON hosting_build_log_receipts FOR EACH ROW EXECUTE FUNCTION nerocd_build_log_immutable();
CREATE FUNCTION nerocd_hosting_build_log_schema_compatible() RETURNS boolean LANGUAGE sql STABLE AS $$
SELECT to_regclass(current_schema()||'.hosting_build_log_states') IS NOT NULL
 AND to_regclass(current_schema()||'.hosting_build_log_receipts') IS NOT NULL
 AND EXISTS(SELECT 1 FROM pg_attribute WHERE attrelid=to_regclass(current_schema()||'.run_logs') AND attname='component' AND NOT attisdropped)
 AND EXISTS(SELECT 1 FROM pg_attribute WHERE attrelid=to_regclass(current_schema()||'.run_logs') AND attname='source_timestamp' AND NOT attisdropped)
 AND EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass(current_schema()||'.hosting_build_tasks') AND tgname='hosting_build_log_dispatch' AND tgenabled IN('O','A'))
 AND EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass(current_schema()||'.hosting_build_log_states') AND tgname='hosting_build_log_state_identity' AND tgenabled IN('O','A'))
 AND EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass(current_schema()||'.hosting_build_log_receipts') AND tgname='hosting_build_log_receipt_identity' AND tgenabled IN('O','A'));
$$;
DO $$ DECLARE owner_schema text:=current_schema(); BEGIN
 EXECUTE format('ALTER FUNCTION %I.nerocd_build_log_dispatch() SET search_path TO %I, pg_catalog, pg_temp',owner_schema,owner_schema);
 EXECUTE format('ALTER FUNCTION %I.nerocd_build_log_immutable() SET search_path TO %I, pg_catalog, pg_temp',owner_schema,owner_schema);
 EXECUTE format('ALTER FUNCTION %I.nerocd_hosting_build_log_schema_compatible() SET search_path TO %I, pg_catalog, pg_temp',owner_schema,owner_schema);
END $$;
