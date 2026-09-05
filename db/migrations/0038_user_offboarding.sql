CREATE INDEX IF NOT EXISTS users_created_page_idx ON users (created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS users_active_admin_idx ON users (id) WHERE status = 'active' AND global_role = 'system_admin';
CREATE INDEX IF NOT EXISTS api_tokens_active_issuer_idx ON api_tokens (created_by) WHERE status = 'active' AND revoked_at IS NULL;

CREATE OR REPLACE FUNCTION nerocd_user_offboarding_schema_compatible()
RETURNS boolean
LANGUAGE sql
STABLE
AS $function$
SELECT to_regclass(current_schema() || '.users_created_page_idx') IS NOT NULL
   AND to_regclass(current_schema() || '.users_active_admin_idx') IS NOT NULL
   AND to_regclass(current_schema() || '.api_tokens_active_issuer_idx') IS NOT NULL;
$function$;
