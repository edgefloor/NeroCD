-- A binding retains one pending challenge and one accepted observation, never a log.
CREATE TABLE hosting_runtime_observations (
 binding_id text PRIMARY KEY REFERENCES resource_bindings(id) ON DELETE CASCADE CHECK(octet_length(binding_id) BETWEEN 1 AND 128),
 pending_target jsonb CHECK(pending_target IS NULL OR (jsonb_typeof(pending_target)='object' AND octet_length(pending_target::text)<=8192)),
 pending_identity text NOT NULL DEFAULT '' CHECK(pending_identity='' OR pending_identity ~ '^sha256:[0-9a-f]{64}$'),
 accepted_target jsonb CHECK(accepted_target IS NULL OR (jsonb_typeof(accepted_target)='object' AND octet_length(accepted_target::text)<=8192)),
 accepted_identity text NOT NULL DEFAULT '' CHECK(accepted_identity='' OR accepted_identity ~ '^sha256:[0-9a-f]{64}$'),
 accepted_body jsonb CHECK(accepted_body IS NULL OR (jsonb_typeof(accepted_body)='object' AND octet_length(accepted_body::text)<=8192)),
 accepted_ack jsonb CHECK(accepted_ack IS NULL OR (jsonb_typeof(accepted_ack)='object' AND octet_length(accepted_ack::text)<=8192)),
 CHECK((accepted_target IS NULL)=(accepted_body IS NULL) AND (accepted_body IS NULL)=(accepted_ack IS NULL))
);

CREATE FUNCTION nerocd_hosting_observation_schema_compatible() RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT to_regclass(current_schema()||'.hosting_runtime_observations') IS NOT NULL
 AND EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid=to_regclass(current_schema()||'.hosting_runtime_observations') AND contype='p')
 AND EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid=to_regclass(current_schema()||'.hosting_runtime_observations') AND contype='f' AND confdeltype='c' AND convalidated)
 AND (SELECT count(*) FROM pg_constraint WHERE conrelid=to_regclass(current_schema()||'.hosting_runtime_observations') AND contype='c' AND convalidated)=8;
$$;
DO $migration$
BEGIN
 EXECUTE format('ALTER FUNCTION %I.nerocd_hosting_observation_schema_compatible() SET search_path TO %I, pg_catalog, pg_temp',current_schema(),current_schema());
END;$migration$;
