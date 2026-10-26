CREATE TABLE hosting_notification_channels (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 80),
    type TEXT NOT NULL CHECK (type IN ('email','webhook')),
    enabled BOOLEAN NOT NULL,
    generation BIGINT NOT NULL CHECK (generation > 0),
    events TEXT[] NOT NULL CHECK (events <@ ARRAY['backup_failed','upload_failed']::TEXT[] AND cardinality(events)<=2),
    secret_id TEXT NOT NULL,
    secret_version BIGINT NOT NULL CHECK (secret_version>0),
    consecutive_failures INTEGER NOT NULL DEFAULT 0 CHECK (consecutive_failures BETWEEN 0 AND 3),
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    UNIQUE(id, project_id)
);
CREATE TABLE hosting_notification_secrets (
    id TEXT NOT NULL,
    version BIGINT NOT NULL CHECK (version>0),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    status TEXT NOT NULL CHECK (status IN ('active','revoked')),
    envelope_version INTEGER NOT NULL CHECK (envelope_version=1),
    key_id TEXT NOT NULL,
    ciphertext BYTEA NOT NULL CHECK (octet_length(ciphertext) BETWEEN 29 AND 65564),
    PRIMARY KEY(id,version),
    UNIQUE(id,version,project_id)
);
ALTER TABLE hosting_notification_channels ADD FOREIGN KEY(secret_id,secret_version,project_id) REFERENCES hosting_notification_secrets(id,version,project_id);
CREATE TABLE hosting_notification_requests (
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    request_id TEXT NOT NULL,
    actor_id TEXT NOT NULL,
    operation TEXT NOT NULL CHECK(operation IN ('save','test')),
    request_hash TEXT NOT NULL,
    secret_id TEXT NOT NULL DEFAULT '',
    secret_version BIGINT NOT NULL DEFAULT 0,
    response JSONB NOT NULL,
    PRIMARY KEY(project_id,request_id)
);
CREATE TABLE hosting_notification_source_receipts (
    id TEXT PRIMARY KEY,
    source_kind TEXT NOT NULL CHECK(source_kind IN ('backup','transfer')),
    source_id TEXT NOT NULL,
    reason TEXT NOT NULL,
    source_snapshot JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE hosting_notification_events (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    event TEXT NOT NULL CHECK(event IN ('backup_failed','upload_failed','test')),
    source_kind TEXT NOT NULL CHECK(source_kind IN ('backup','transfer','test')),
    source_id TEXT NOT NULL,
    terminal_event TEXT NOT NULL CHECK(terminal_event=event),
    source_state TEXT NOT NULL CHECK(source_state IN ('failed','uncertain','test')),
    source_receipt TEXT NOT NULL,
    failure_code TEXT NOT NULL,
    local_copy_available BOOLEAN NOT NULL,
    recovery_url TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    UNIQUE(source_kind,source_id,terminal_event),
    UNIQUE(id,project_id)
);
CREATE TABLE hosting_notification_deliveries (
    id TEXT PRIMARY KEY,
    event_id TEXT NOT NULL,
    project_id TEXT NOT NULL,
    channel_id TEXT NOT NULL,
    channel_generation BIGINT NOT NULL CHECK(channel_generation>0),
    secret_id TEXT NOT NULL,
    secret_version BIGINT NOT NULL,
    state TEXT NOT NULL CHECK(state IN ('pending','sending','delivered','failed','suppressed')),
    attempt_count INTEGER NOT NULL DEFAULT 0 CHECK(attempt_count BETWEEN 0 AND 5),
    failure_code TEXT NOT NULL DEFAULT '',
    fence TEXT NOT NULL DEFAULT '',
    claim_expires_at TIMESTAMPTZ,
    deadline_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()+INTERVAL '1 hour',
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    FOREIGN KEY(event_id,project_id) REFERENCES hosting_notification_events(id,project_id),
    FOREIGN KEY(channel_id,project_id) REFERENCES hosting_notification_channels(id,project_id),
    FOREIGN KEY(secret_id,secret_version,project_id) REFERENCES hosting_notification_secrets(id,version,project_id),
    UNIQUE(event_id,channel_id,channel_generation)
);
CREATE INDEX hosting_notification_pending ON hosting_notification_deliveries(next_attempt_at,id) WHERE state IN ('pending','sending');
CREATE INDEX hosting_notification_history ON hosting_notification_deliveries(project_id,created_at DESC,id DESC);
CREATE TABLE hosting_notification_attempts (
    delivery_id TEXT NOT NULL REFERENCES hosting_notification_deliveries(id),
    number INTEGER NOT NULL CHECK(number BETWEEN 1 AND 5),
    fence TEXT NOT NULL,
    state TEXT NOT NULL CHECK(state IN ('sending','delivered','failed','uncertain','suppressed')),
    failure_code TEXT NOT NULL DEFAULT '',
    started_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    finished_at TIMESTAMPTZ,
    PRIMARY KEY(delivery_id,number)
);

CREATE FUNCTION nerocd_notification_event_source() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP<>'INSERT' THEN RAISE EXCEPTION 'notification event immutable' USING ERRCODE='23514'; END IF;
 IF NEW.source_kind='backup' AND NEW.source_state IS DISTINCT FROM (SELECT state FROM hosting_database_backups WHERE id=NEW.source_id) THEN RAISE EXCEPTION 'notification backup state mismatch' USING ERRCODE='23514'; END IF;
 IF NEW.source_kind='transfer' AND NEW.source_state IS DISTINCT FROM (SELECT state FROM hosting_database_backup_transfers WHERE id=NEW.source_id) THEN RAISE EXCEPTION 'notification upload state mismatch' USING ERRCODE='23514'; END IF;
 IF (NEW.source_kind='test') IS DISTINCT FROM (NEW.source_state='test') THEN RAISE EXCEPTION 'notification test state mismatch' USING ERRCODE='23514'; END IF;
 IF NEW.source_kind='backup' THEN
  IF NEW.event<>'backup_failed' OR NOT EXISTS(SELECT 1 FROM hosting_database_backups b WHERE b.id=NEW.source_id AND b.project_id=NEW.project_id AND b.mode='backup' AND b.state IN ('failed','uncertain') AND b.failure_code=NEW.failure_code AND (EXISTS(SELECT 1 FROM hosting_database_backup_receipts r WHERE r.operation_id=b.id AND r.receipt_id=NEW.source_receipt AND r.phase IN ('failed','uncertain')) OR EXISTS(SELECT 1 FROM hosting_notification_source_receipts r WHERE r.id=NEW.source_receipt AND r.source_kind='backup' AND r.source_id=b.id AND r.source_snapshot=jsonb_build_object('state',b.state,'failure_code',b.failure_code,'plan_hash',b.plan_hash)))) THEN RAISE EXCEPTION 'notification backup source mismatch' USING ERRCODE='23514'; END IF;
 ELSIF NEW.source_kind='transfer' THEN
  IF NEW.event<>'upload_failed' OR NOT EXISTS(SELECT 1 FROM hosting_database_backup_transfers t WHERE t.id=NEW.source_id AND t.project_id=NEW.project_id AND t.mode='upload' AND t.state IN ('failed','uncertain') AND t.failure_code=NEW.failure_code AND (EXISTS(SELECT 1 FROM hosting_database_backup_transfer_receipts r WHERE r.operation_id=t.id AND r.receipt_id=NEW.source_receipt AND r.phase IN ('failed','uncertain')) OR EXISTS(SELECT 1 FROM hosting_notification_source_receipts r WHERE r.id=NEW.source_receipt AND r.source_kind='transfer' AND r.source_id=t.id AND r.source_snapshot=jsonb_build_object('state',t.state,'failure_code',t.failure_code,'plan_hash',t.plan_hash)))) THEN RAISE EXCEPTION 'notification upload source mismatch' USING ERRCODE='23514'; END IF;
 ELSIF NEW.event<>'test' THEN RAISE EXCEPTION 'notification test source mismatch' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER hosting_notification_event_source BEFORE INSERT OR UPDATE OR DELETE ON hosting_notification_events FOR EACH ROW EXECUTE FUNCTION nerocd_notification_event_source();
CREATE TRIGGER hosting_notification_source_receipts_immutable BEFORE UPDATE OR DELETE ON hosting_notification_source_receipts FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_execution_immutable();
CREATE TRIGGER hosting_notification_requests_immutable BEFORE UPDATE OR DELETE ON hosting_notification_requests FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_execution_immutable();
