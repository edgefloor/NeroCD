CREATE TABLE runner_enrollments (
    id TEXT PRIMARY KEY,
    token_hash TEXT NOT NULL UNIQUE,
    runner_id TEXT NOT NULL UNIQUE,
    runner_name TEXT NOT NULL,
    tags TEXT[] NOT NULL DEFAULT '{}',
    capabilities TEXT[] NOT NULL DEFAULT '{}',
    created_by TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    expires_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ,
    used_at TIMESTAMPTZ,
    consume_request_id TEXT,
    credential_hash TEXT,
    CONSTRAINT runner_enrollments_token_hash_nonempty CHECK (token_hash <> ''),
    CONSTRAINT runner_enrollments_runner_id_nonempty CHECK (runner_id <> ''),
    CONSTRAINT runner_enrollments_runner_name_nonempty CHECK (runner_name <> ''),
    CONSTRAINT runner_enrollments_capabilities_nonempty CHECK (cardinality(capabilities) > 0),
    CONSTRAINT runner_enrollments_expiry_order CHECK (expires_at > created_at),
    CONSTRAINT runner_enrollments_consumption_complete CHECK (
        (used_at IS NULL AND consume_request_id IS NULL AND credential_hash IS NULL)
        OR
        (used_at IS NOT NULL AND consume_request_id IS NOT NULL AND consume_request_id <> '' AND credential_hash IS NOT NULL AND credential_hash <> '')
    )
);

CREATE INDEX idx_runner_enrollments_expires_at ON runner_enrollments (expires_at) WHERE used_at IS NULL AND revoked_at IS NULL;

CREATE OR REPLACE FUNCTION nerocd_enrollment_schema_compatible()
RETURNS boolean
LANGUAGE sql
STABLE
AS $function$
SELECT to_regclass(current_schema() || '.runner_enrollments') IS NOT NULL
  AND EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema=current_schema() AND table_name='runner_enrollments'
      AND column_name='credential_hash' AND is_nullable='YES'
  )
  AND EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema=current_schema() AND table_name='runner_enrollments'
      AND column_name='consume_request_id' AND is_nullable='YES'
  );
$function$;
