CREATE TABLE IF NOT EXISTS oidc_external_identities (
    issuer TEXT NOT NULL,
    subject TEXT NOT NULL,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (issuer, subject),
    UNIQUE (user_id, issuer)
);

CREATE TABLE IF NOT EXISTS oidc_login_flows (
    id TEXT PRIMARY KEY,
    state_hash TEXT NOT NULL UNIQUE,
    nonce_hash TEXT NOT NULL,
    verifier_hash TEXT NOT NULL,
    redirect_path TEXT NOT NULL,
    issuer TEXT NOT NULL,
    client_id TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);

CREATE INDEX IF NOT EXISTS idx_oidc_login_flows_expires_at
    ON oidc_login_flows(expires_at);

CREATE OR REPLACE FUNCTION nerocd_oidc_schema_compatible()
RETURNS boolean
LANGUAGE sql
STABLE
AS $function$
SELECT to_regclass(current_schema() || '.oidc_external_identities') IS NOT NULL
   AND to_regclass(current_schema() || '.oidc_login_flows') IS NOT NULL
   AND EXISTS (
       SELECT 1
       FROM information_schema.columns
       WHERE table_schema = current_schema()
         AND table_name = 'oidc_login_flows'
         AND column_name IN ('state_hash', 'nonce_hash', 'verifier_hash', 'issuer', 'client_id', 'expires_at')
       GROUP BY table_schema, table_name
       HAVING count(*) = 6
   );
$function$;
