-- Legacy API tokens authenticate as their durable token ID. Managed credentials
-- authenticate as their service account and must not become separate actors.
ALTER TABLE principals DROP CONSTRAINT principals_kind_check;
ALTER TABLE principals ADD CONSTRAINT principals_kind_check
    CHECK (kind IN ('human', 'service_account', 'api_token'));

DO $block$
BEGIN
    IF EXISTS (SELECT 1 FROM api_tokens t JOIN principals p ON p.id = t.id
               WHERE t.service_account_id IS NULL AND p.kind <> 'api_token') THEN
        RAISE EXCEPTION 'legacy API token principal id collides with another kind';
    END IF;
END;
$block$;

INSERT INTO principals (id, kind, created_at)
SELECT id, 'api_token', created_at FROM api_tokens WHERE service_account_id IS NULL
ON CONFLICT (id) DO NOTHING;

-- Check after insertion: concurrent cross-kind inserts must not silently accept
-- the winning transaction's registry row through ON CONFLICT DO NOTHING.
CREATE OR REPLACE FUNCTION nerocd_register_principal()
RETURNS trigger LANGUAGE plpgsql AS $function$
DECLARE expected_kind TEXT := TG_ARGV[0]; existing_kind TEXT;
BEGIN
    INSERT INTO principals (id, kind, created_at)
    VALUES (NEW.id, expected_kind, COALESCE(NEW.created_at, clock_timestamp()))
    ON CONFLICT (id) DO NOTHING;
    SELECT kind INTO existing_kind FROM principals WHERE id = NEW.id;
    IF existing_kind IS DISTINCT FROM expected_kind THEN
        RAISE EXCEPTION 'principal id already registered with another kind';
    END IF;
    RETURN NEW;
END;
$function$;

CREATE FUNCTION nerocd_register_legacy_api_token_principal()
RETURNS trigger LANGUAGE plpgsql AS $function$
DECLARE existing_kind TEXT;
BEGIN
    IF NEW.service_account_id IS NOT NULL THEN
        RETURN NEW;
    END IF;
    INSERT INTO principals (id, kind, created_at)
    VALUES (NEW.id, 'api_token', NEW.created_at)
    ON CONFLICT (id) DO NOTHING;
    SELECT kind INTO existing_kind FROM principals WHERE id = NEW.id;
    IF existing_kind IS DISTINCT FROM 'api_token' THEN
        RAISE EXCEPTION 'principal id already registered with another kind';
    END IF;
    RETURN NEW;
END;
$function$;
CREATE TRIGGER api_tokens_register_principal BEFORE INSERT ON api_tokens
FOR EACH ROW EXECUTE FUNCTION nerocd_register_legacy_api_token_principal();

CREATE FUNCTION nerocd_api_token_immutable_identity()
RETURNS trigger LANGUAGE plpgsql AS $function$
BEGIN
    IF NEW.id IS DISTINCT FROM OLD.id OR NEW.service_account_id IS DISTINCT FROM OLD.service_account_id THEN
        RAISE EXCEPTION 'API token identity and service account association are immutable';
    END IF;
    RETURN NEW;
END;
$function$;
CREATE TRIGGER api_tokens_immutable_identity BEFORE UPDATE ON api_tokens
FOR EACH ROW EXECUTE FUNCTION nerocd_api_token_immutable_identity();

CREATE FUNCTION nerocd_legacy_api_token_schema_compatible()
RETURNS boolean LANGUAGE sql STABLE AS $function$
SELECT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'principals'::regclass
    AND conname = 'principals_kind_check' AND contype = 'c' AND convalidated
    AND pg_get_constraintdef(oid) LIKE '%api_token%')
AND EXISTS (SELECT 1 FROM pg_trigger WHERE tgrelid = 'api_tokens'::regclass
    AND tgname = 'api_tokens_register_principal' AND tgenabled <> 'D')
AND EXISTS (SELECT 1 FROM pg_trigger WHERE tgrelid = 'api_tokens'::regclass
    AND tgname = 'api_tokens_immutable_identity' AND tgenabled <> 'D')
AND NOT EXISTS (SELECT 1 FROM api_tokens t LEFT JOIN principals p ON p.id = t.id
    WHERE t.service_account_id IS NULL AND (p.kind IS DISTINCT FROM 'api_token'));
$function$;
