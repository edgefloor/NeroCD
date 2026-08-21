-- A single durable row serializes the only supported local-identity bootstrap.
-- It is deliberately additive: installations that already contain users are
-- marked completed and can never accidentally receive a second administrator.
CREATE TABLE IF NOT EXISTS identity_bootstrap_state (
    singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
    completed_by TEXT,
    completed_at TIMESTAMPTZ
);

INSERT INTO identity_bootstrap_state (singleton)
VALUES (TRUE)
ON CONFLICT (singleton) DO NOTHING;

UPDATE identity_bootstrap_state
SET completed_by = 'preexisting', completed_at = clock_timestamp()
WHERE singleton = TRUE
  AND completed_by IS NULL
  AND EXISTS (SELECT 1 FROM users);

CREATE OR REPLACE FUNCTION nerocd_identity_schema_compatible()
RETURNS boolean
LANGUAGE sql
STABLE
AS $function$
SELECT to_regclass(current_schema() || '.identity_bootstrap_state') IS NOT NULL
   AND EXISTS (SELECT 1 FROM identity_bootstrap_state WHERE singleton = TRUE);
$function$;
