-- GitHub.com push policies admit pinned builds only; deployment remains manual.
CREATE TABLE hosting_github_push_policies (
 id text PRIMARY KEY, resource_id text NOT NULL UNIQUE REFERENCES resources(id) ON DELETE CASCADE,
 project_id text NOT NULL REFERENCES projects(id), generation bigint NOT NULL CHECK(generation>0),
 body jsonb NOT NULL CHECK(jsonb_typeof(body)='object' AND octet_length(body::text)<=16384),
 verification_token text NOT NULL DEFAULT '', verification_expires_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE hosting_github_push_commands (
 resource_id text NOT NULL REFERENCES resources(id) ON DELETE CASCADE, request_id text NOT NULL,
 actor_id text NOT NULL REFERENCES principals(id), kind text NOT NULL CHECK(kind IN('configure','verify','enabled','retry')),
 body_hash text NOT NULL, expected_generation bigint NOT NULL CHECK(expected_generation>=0),
 token text NOT NULL DEFAULT '', expires_at timestamptz NOT NULL DEFAULT clock_timestamp()+interval '30 seconds',
 receipt jsonb CHECK(receipt IS NULL OR octet_length(receipt::text)<=16384),
 PRIMARY KEY(resource_id,request_id)
);
CREATE TABLE hosting_github_push_deliveries (
 id text PRIMARY KEY,policy_id text NOT NULL REFERENCES hosting_github_push_policies(id) ON DELETE CASCADE,
 policy_generation bigint NOT NULL,resource_id text NOT NULL REFERENCES resources(id) ON DELETE CASCADE,
 project_id text NOT NULL REFERENCES projects(id),delivery_id text NOT NULL,body_hash text NOT NULL,
 after_commit text NOT NULL, verified_commit text NOT NULL DEFAULT '',build_id text NOT NULL DEFAULT '',
 state text NOT NULL CHECK(state IN('received','verifying','retry_wait','queued','coalesced','ignored','obsolete','blocked_exhausted')),
 reason text NOT NULL DEFAULT '',attempts integer NOT NULL DEFAULT 0 CHECK(attempts BETWEEN 0 AND 32),
 token text NOT NULL DEFAULT '',token_expires_at timestamptz,
 received_at timestamptz NOT NULL DEFAULT clock_timestamp(),next_retry_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 expires_at timestamptz NOT NULL DEFAULT clock_timestamp()+interval '24 hours',
 retain_until timestamptz NOT NULL DEFAULT clock_timestamp()+interval '72 hours',
 UNIQUE(policy_id,delivery_id)
);
CREATE INDEX hosting_github_push_due ON hosting_github_push_deliveries(state,next_retry_at,id);
CREATE INDEX hosting_github_push_retention ON hosting_github_push_deliveries(retain_until,id);
CREATE INDEX hosting_github_push_scope ON hosting_github_push_deliveries(project_id,resource_id,received_at,id);
-- This identity survives delivery receipt cleanup. It never depends on River retention.
CREATE TABLE hosting_github_push_commit_intents (
 policy_id text NOT NULL REFERENCES hosting_github_push_policies(id) ON DELETE CASCADE,
 policy_generation bigint NOT NULL, source_generation bigint NOT NULL,commit_sha text NOT NULL,
 build_id text NOT NULL REFERENCES hosting_builds(id),created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(policy_id,policy_generation,source_generation,commit_sha),UNIQUE(build_id)
);
CREATE FUNCTION nerocd_hosting_github_push_schema_compatible() RETURNS boolean LANGUAGE sql STABLE AS $$
 WITH required(table_name,column_name,type_name,not_null) AS (VALUES
('hosting_github_push_policies','id','text',true),
('hosting_github_push_policies','resource_id','text',true),
('hosting_github_push_policies','project_id','text',true),
('hosting_github_push_policies','generation','bigint',true),
('hosting_github_push_policies','body','jsonb',true),
('hosting_github_push_policies','verification_token','text',true),
('hosting_github_push_policies','verification_expires_at','timestamp with time zone',false),
('hosting_github_push_policies','created_at','timestamp with time zone',true),
('hosting_github_push_commands','resource_id','text',true),
('hosting_github_push_commands','request_id','text',true),
('hosting_github_push_commands','actor_id','text',true),
('hosting_github_push_commands','kind','text',true),
('hosting_github_push_commands','body_hash','text',true),
('hosting_github_push_commands','expected_generation','bigint',true),
('hosting_github_push_commands','token','text',true),
('hosting_github_push_commands','expires_at','timestamp with time zone',true),
('hosting_github_push_commands','receipt','jsonb',false),
('hosting_github_push_deliveries','id','text',true),
('hosting_github_push_deliveries','policy_id','text',true),
('hosting_github_push_deliveries','policy_generation','bigint',true),
('hosting_github_push_deliveries','resource_id','text',true),
('hosting_github_push_deliveries','project_id','text',true),
('hosting_github_push_deliveries','delivery_id','text',true),
('hosting_github_push_deliveries','body_hash','text',true),
('hosting_github_push_deliveries','after_commit','text',true),
('hosting_github_push_deliveries','verified_commit','text',true),
('hosting_github_push_deliveries','build_id','text',true),
('hosting_github_push_deliveries','state','text',true),
('hosting_github_push_deliveries','reason','text',true),
('hosting_github_push_deliveries','attempts','integer',true),
('hosting_github_push_deliveries','token','text',true),
('hosting_github_push_deliveries','token_expires_at','timestamp with time zone',false),
('hosting_github_push_deliveries','received_at','timestamp with time zone',true),
('hosting_github_push_deliveries','next_retry_at','timestamp with time zone',true),
('hosting_github_push_deliveries','expires_at','timestamp with time zone',true),
('hosting_github_push_deliveries','retain_until','timestamp with time zone',true),
('hosting_github_push_commit_intents','policy_id','text',true),
('hosting_github_push_commit_intents','policy_generation','bigint',true),
('hosting_github_push_commit_intents','source_generation','bigint',true),
('hosting_github_push_commit_intents','commit_sha','text',true),
('hosting_github_push_commit_intents','build_id','text',true),
('hosting_github_push_commit_intents','created_at','timestamp with time zone',true))
 SELECT NOT EXISTS(SELECT 1 FROM required r LEFT JOIN pg_attribute a ON a.attrelid=to_regclass(current_schema()||'.'||r.table_name) AND a.attname=r.column_name AND a.attnum>0 AND NOT a.attisdropped WHERE a.attname IS NULL OR a.atttypid<>to_regtype(r.type_name) OR a.attnotnull<>r.not_null)
 AND EXISTS(SELECT 1 FROM pg_constraint c WHERE c.conrelid=to_regclass(current_schema()||'.hosting_github_push_policies') AND c.conname='hosting_github_push_policies_generation_check' AND c.contype='c' AND c.convalidated AND translate(pg_get_expr(c.conbin,c.conrelid),E' \n\r\t()','')='generation>0')
 AND EXISTS(SELECT 1 FROM pg_constraint c WHERE c.conrelid=to_regclass(current_schema()||'.hosting_github_push_policies') AND c.conname='hosting_github_push_policies_body_check' AND c.contype='c' AND c.convalidated AND translate(pg_get_expr(c.conbin,c.conrelid),E' \n\r\t()','')='jsonb_typeofbody=''object''::textANDoctet_lengthbody::text<=16384')
 AND EXISTS(SELECT 1 FROM pg_constraint c WHERE c.conrelid=to_regclass(current_schema()||'.hosting_github_push_commands') AND c.conname='hosting_github_push_commands_expected_generation_check' AND c.contype='c' AND c.convalidated AND translate(pg_get_expr(c.conbin,c.conrelid),E' \n\r\t()','')='expected_generation>=0')
 AND EXISTS(SELECT 1 FROM pg_constraint c WHERE c.conrelid=to_regclass(current_schema()||'.hosting_github_push_commands') AND c.conname='hosting_github_push_commands_receipt_check' AND c.contype='c' AND c.convalidated AND translate(pg_get_expr(c.conbin,c.conrelid),E' \n\r\t()','')='receiptISNULLORoctet_lengthreceipt::text<=16384')
 AND EXISTS(SELECT 1 FROM pg_constraint c WHERE c.conrelid=to_regclass(current_schema()||'.hosting_github_push_commands') AND c.conname='hosting_github_push_commands_kind_check' AND c.contype='c' AND c.convalidated AND translate(pg_get_expr(c.conbin,c.conrelid),E' \n\r\t()','')='kind=ANYARRAY[''configure''::text,''verify''::text,''enabled''::text,''retry''::text]')
 AND EXISTS(SELECT 1 FROM pg_constraint c WHERE c.conrelid=to_regclass(current_schema()||'.hosting_github_push_deliveries') AND c.conname='hosting_github_push_deliveries_attempts_check' AND c.contype='c' AND c.convalidated AND translate(pg_get_expr(c.conbin,c.conrelid),E' \n\r\t()','')='attempts>=0ANDattempts<=32')
 AND EXISTS(SELECT 1 FROM pg_constraint c WHERE c.conrelid=to_regclass(current_schema()||'.hosting_github_push_deliveries') AND c.conname='hosting_github_push_deliveries_state_check' AND c.contype='c' AND c.convalidated AND translate(pg_get_expr(c.conbin,c.conrelid),E' \n\r\t()','')='state=ANYARRAY[''received''::text,''verifying''::text,''retry_wait''::text,''queued''::text,''coalesced''::text,''ignored''::text,''obsolete''::text,''blocked_exhausted''::text]')
 AND EXISTS(SELECT 1 FROM pg_constraint c WHERE c.conrelid=to_regclass(current_schema()||'.hosting_github_push_policies') AND c.contype='p' AND c.convalidated AND ARRAY(SELECT a.attname::text FROM unnest(c.conkey) WITH ORDINALITY k(attnum,n) JOIN pg_attribute a ON a.attrelid=c.conrelid AND a.attnum=k.attnum ORDER BY k.n)=ARRAY['id']::text[])
 AND EXISTS(SELECT 1 FROM pg_constraint c WHERE c.conrelid=to_regclass(current_schema()||'.hosting_github_push_policies') AND c.contype='u' AND c.convalidated AND ARRAY(SELECT a.attname::text FROM unnest(c.conkey) WITH ORDINALITY k(attnum,n) JOIN pg_attribute a ON a.attrelid=c.conrelid AND a.attnum=k.attnum ORDER BY k.n)=ARRAY['resource_id']::text[])
 AND EXISTS(SELECT 1 FROM pg_constraint c WHERE c.conrelid=to_regclass(current_schema()||'.hosting_github_push_commands') AND c.contype='p' AND c.convalidated AND ARRAY(SELECT a.attname::text FROM unnest(c.conkey) WITH ORDINALITY k(attnum,n) JOIN pg_attribute a ON a.attrelid=c.conrelid AND a.attnum=k.attnum ORDER BY k.n)=ARRAY['resource_id','request_id']::text[])
 AND EXISTS(SELECT 1 FROM pg_constraint c WHERE c.conrelid=to_regclass(current_schema()||'.hosting_github_push_deliveries') AND c.contype='p' AND c.convalidated AND ARRAY(SELECT a.attname::text FROM unnest(c.conkey) WITH ORDINALITY k(attnum,n) JOIN pg_attribute a ON a.attrelid=c.conrelid AND a.attnum=k.attnum ORDER BY k.n)=ARRAY['id']::text[])
 AND EXISTS(SELECT 1 FROM pg_constraint c WHERE c.conrelid=to_regclass(current_schema()||'.hosting_github_push_deliveries') AND c.contype='u' AND c.convalidated AND ARRAY(SELECT a.attname::text FROM unnest(c.conkey) WITH ORDINALITY k(attnum,n) JOIN pg_attribute a ON a.attrelid=c.conrelid AND a.attnum=k.attnum ORDER BY k.n)=ARRAY['policy_id','delivery_id']::text[])
 AND EXISTS(SELECT 1 FROM pg_constraint c WHERE c.conrelid=to_regclass(current_schema()||'.hosting_github_push_commit_intents') AND c.contype='p' AND c.convalidated AND ARRAY(SELECT a.attname::text FROM unnest(c.conkey) WITH ORDINALITY k(attnum,n) JOIN pg_attribute a ON a.attrelid=c.conrelid AND a.attnum=k.attnum ORDER BY k.n)=ARRAY['policy_id','policy_generation','source_generation','commit_sha']::text[])
 AND EXISTS(SELECT 1 FROM pg_constraint c WHERE c.conrelid=to_regclass(current_schema()||'.hosting_github_push_commit_intents') AND c.contype='u' AND c.convalidated AND ARRAY(SELECT a.attname::text FROM unnest(c.conkey) WITH ORDINALITY k(attnum,n) JOIN pg_attribute a ON a.attrelid=c.conrelid AND a.attnum=k.attnum ORDER BY k.n)=ARRAY['build_id']::text[])
 AND EXISTS(SELECT 1 FROM pg_constraint c JOIN pg_attribute a ON a.attrelid=c.conrelid AND c.conkey=ARRAY[a.attnum] WHERE c.conrelid=to_regclass(current_schema()||'.hosting_github_push_policies') AND a.attname='resource_id' AND c.contype='f' AND c.convalidated AND c.confrelid=to_regclass(current_schema()||'.resources') AND c.confdeltype='c')
 AND EXISTS(SELECT 1 FROM pg_constraint c JOIN pg_attribute a ON a.attrelid=c.conrelid AND c.conkey=ARRAY[a.attnum] WHERE c.conrelid=to_regclass(current_schema()||'.hosting_github_push_policies') AND a.attname='project_id' AND c.contype='f' AND c.convalidated AND c.confrelid=to_regclass(current_schema()||'.projects') AND c.confdeltype='a')
 AND EXISTS(SELECT 1 FROM pg_constraint c JOIN pg_attribute a ON a.attrelid=c.conrelid AND c.conkey=ARRAY[a.attnum] WHERE c.conrelid=to_regclass(current_schema()||'.hosting_github_push_commands') AND a.attname='resource_id' AND c.contype='f' AND c.convalidated AND c.confrelid=to_regclass(current_schema()||'.resources') AND c.confdeltype='c')
 AND EXISTS(SELECT 1 FROM pg_constraint c JOIN pg_attribute a ON a.attrelid=c.conrelid AND c.conkey=ARRAY[a.attnum] WHERE c.conrelid=to_regclass(current_schema()||'.hosting_github_push_commands') AND a.attname='actor_id' AND c.contype='f' AND c.convalidated AND c.confrelid=to_regclass(current_schema()||'.principals') AND c.confdeltype='a')
 AND EXISTS(SELECT 1 FROM pg_constraint c JOIN pg_attribute a ON a.attrelid=c.conrelid AND c.conkey=ARRAY[a.attnum] WHERE c.conrelid=to_regclass(current_schema()||'.hosting_github_push_deliveries') AND a.attname='policy_id' AND c.contype='f' AND c.convalidated AND c.confrelid=to_regclass(current_schema()||'.hosting_github_push_policies') AND c.confdeltype='c')
 AND EXISTS(SELECT 1 FROM pg_constraint c JOIN pg_attribute a ON a.attrelid=c.conrelid AND c.conkey=ARRAY[a.attnum] WHERE c.conrelid=to_regclass(current_schema()||'.hosting_github_push_deliveries') AND a.attname='resource_id' AND c.contype='f' AND c.convalidated AND c.confrelid=to_regclass(current_schema()||'.resources') AND c.confdeltype='c')
 AND EXISTS(SELECT 1 FROM pg_constraint c JOIN pg_attribute a ON a.attrelid=c.conrelid AND c.conkey=ARRAY[a.attnum] WHERE c.conrelid=to_regclass(current_schema()||'.hosting_github_push_deliveries') AND a.attname='project_id' AND c.contype='f' AND c.convalidated AND c.confrelid=to_regclass(current_schema()||'.projects') AND c.confdeltype='a')
 AND EXISTS(SELECT 1 FROM pg_constraint c JOIN pg_attribute a ON a.attrelid=c.conrelid AND c.conkey=ARRAY[a.attnum] WHERE c.conrelid=to_regclass(current_schema()||'.hosting_github_push_commit_intents') AND a.attname='policy_id' AND c.contype='f' AND c.convalidated AND c.confrelid=to_regclass(current_schema()||'.hosting_github_push_policies') AND c.confdeltype='c')
 AND EXISTS(SELECT 1 FROM pg_constraint c JOIN pg_attribute a ON a.attrelid=c.conrelid AND c.conkey=ARRAY[a.attnum] WHERE c.conrelid=to_regclass(current_schema()||'.hosting_github_push_commit_intents') AND a.attname='build_id' AND c.contype='f' AND c.convalidated AND c.confrelid=to_regclass(current_schema()||'.hosting_builds') AND c.confdeltype='a')
 AND EXISTS(SELECT 1 FROM pg_index i WHERE i.indexrelid=to_regclass(current_schema()||'.hosting_github_push_due') AND i.indisvalid AND i.indisready AND ARRAY(SELECT a.attname::text FROM unnest(i.indkey) WITH ORDINALITY k(attnum,n) JOIN pg_attribute a ON a.attrelid=i.indrelid AND a.attnum=k.attnum ORDER BY k.n)=ARRAY['state','next_retry_at','id']::text[]);
$$;
DO $migration$ BEGIN
 EXECUTE format('ALTER FUNCTION %I.nerocd_hosting_github_push_schema_compatible() SET search_path TO %I, pg_catalog, pg_temp',current_schema(),current_schema());
END; $migration$;
