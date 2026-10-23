-- Explicit reviewed automation composes existing build/deployment/proxy authority.
CREATE TABLE hosting_auto_deploy_controls (
 resource_id text PRIMARY KEY REFERENCES resources(id) ON DELETE RESTRICT,
 policy_id text NOT NULL REFERENCES hosting_github_push_policies(id) ON DELETE RESTRICT,
 policy_generation bigint NOT NULL CHECK(policy_generation>0),
 manual_epoch bigint NOT NULL DEFAULT 0 CHECK(manual_epoch>=0),
 body jsonb NOT NULL CONSTRAINT hosting_auto_control_body CHECK(COALESCE(jsonb_typeof(body)='object' AND body->>'version'='1' AND octet_length(body::text)<=65536,false)),
 body_hash text NOT NULL CHECK(body_hash ~ '^sha256:[0-9a-f]{64}$')
);
CREATE TABLE hosting_auto_deploy_demands (
 resource_id text PRIMARY KEY REFERENCES resources(id) ON DELETE RESTRICT,
 id text NOT NULL UNIQUE CHECK(length(id) BETWEEN 1 AND 128),
 policy_id text NOT NULL REFERENCES hosting_github_push_policies(id) ON DELETE RESTRICT,
 policy_generation bigint NOT NULL CHECK(policy_generation>0),
 build_id text NOT NULL REFERENCES hosting_builds(id) ON DELETE RESTRICT,
 commit_sha text NOT NULL CHECK(commit_sha ~ '^[0-9a-f]{40}$'),
 body jsonb NOT NULL CONSTRAINT hosting_auto_demand_body CHECK(COALESCE(jsonb_typeof(body)='object' AND body->>'version'='1' AND octet_length(body::text)<=131072,false)),
 stage text NOT NULL CONSTRAINT hosting_auto_demand_stage CHECK(stage IN ('waiting_build','waiting_deployment','waiting_switch','succeeded','failed','blocked','expired')),
 reason text NOT NULL CHECK(length(reason)<=64),
 attempts integer NOT NULL CHECK(attempts BETWEEN 0 AND 32),
 created_at timestamptz NOT NULL,
 expires_at timestamptz NOT NULL CONSTRAINT hosting_auto_demand_deadline CHECK(expires_at>created_at AND expires_at<=created_at+interval '24 hours'),
 next_retry_at timestamptz NOT NULL,
 updated_at timestamptz NOT NULL,
 last_wakeup_at timestamptz NOT NULL DEFAULT 'epoch',
 observed_loaded_generation bigint NOT NULL DEFAULT 0 CHECK(observed_loaded_generation>=0)
);
CREATE INDEX hosting_auto_deploy_due ON hosting_auto_deploy_demands(stage,next_retry_at,resource_id);
CREATE TABLE hosting_auto_deploy_origins (
 operation_id text PRIMARY KEY,
 kind text NOT NULL CHECK(kind IN ('deployment','switch')),
 resource_id text NOT NULL REFERENCES resources(id) ON DELETE RESTRICT,
 demand_id text NOT NULL CHECK(length(demand_id) BETWEEN 1 AND 128),
 build_id text NOT NULL REFERENCES hosting_builds(id) ON DELETE RESTRICT,
 deployment_operation_id text UNIQUE REFERENCES hosting_operations(id) ON DELETE RESTRICT,
 proxy_operation_id text UNIQUE REFERENCES hosting_proxy_operations(id) ON DELETE RESTRICT,
 body jsonb NOT NULL CONSTRAINT hosting_auto_origin_body CHECK(COALESCE(jsonb_typeof(body)='object' AND body->>'version'='1' AND octet_length(body::text)<=131072,false)),
 body_hash text NOT NULL CHECK(body_hash ~ '^sha256:[0-9a-f]{64}$'),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 CONSTRAINT hosting_auto_origin_kind CHECK((kind='deployment' AND deployment_operation_id IS NOT NULL AND deployment_operation_id=operation_id AND proxy_operation_id IS NULL) OR (kind='switch' AND proxy_operation_id IS NOT NULL AND proxy_operation_id=operation_id AND deployment_operation_id IS NULL))
);
CREATE INDEX hosting_auto_deploy_origin_resource ON hosting_auto_deploy_origins(resource_id,created_at,operation_id);
CREATE TRIGGER hosting_auto_deploy_origin_immutable BEFORE UPDATE OR DELETE ON hosting_auto_deploy_origins FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_execution_immutable();
CREATE FUNCTION nerocd_hosting_auto_deploy_schema_compatible() RETURNS boolean LANGUAGE sql STABLE AS $$
WITH required(table_name,column_name,type_name,not_null) AS (VALUES
('hosting_auto_deploy_controls','resource_id','text',true),
('hosting_auto_deploy_controls','policy_id','text',true),
('hosting_auto_deploy_controls','policy_generation','bigint',true),
('hosting_auto_deploy_controls','manual_epoch','bigint',true),
('hosting_auto_deploy_controls','body','jsonb',true),
('hosting_auto_deploy_controls','body_hash','text',true),
('hosting_auto_deploy_demands','resource_id','text',true),
('hosting_auto_deploy_demands','id','text',true),
('hosting_auto_deploy_demands','policy_id','text',true),
('hosting_auto_deploy_demands','policy_generation','bigint',true),
('hosting_auto_deploy_demands','build_id','text',true),
('hosting_auto_deploy_demands','commit_sha','text',true),
('hosting_auto_deploy_demands','body','jsonb',true),
('hosting_auto_deploy_demands','stage','text',true),
('hosting_auto_deploy_demands','reason','text',true),
('hosting_auto_deploy_demands','attempts','integer',true),
('hosting_auto_deploy_demands','created_at','timestamp with time zone',true),
('hosting_auto_deploy_demands','expires_at','timestamp with time zone',true),
('hosting_auto_deploy_demands','next_retry_at','timestamp with time zone',true),
('hosting_auto_deploy_demands','updated_at','timestamp with time zone',true),
('hosting_auto_deploy_demands','last_wakeup_at','timestamp with time zone',true),
('hosting_auto_deploy_demands','observed_loaded_generation','bigint',true),
('hosting_auto_deploy_origins','operation_id','text',true),
('hosting_auto_deploy_origins','kind','text',true),
('hosting_auto_deploy_origins','resource_id','text',true),
('hosting_auto_deploy_origins','demand_id','text',true),
('hosting_auto_deploy_origins','build_id','text',true),
('hosting_auto_deploy_origins','deployment_operation_id','text',false),
('hosting_auto_deploy_origins','proxy_operation_id','text',false),
('hosting_auto_deploy_origins','body','jsonb',true),
('hosting_auto_deploy_origins','body_hash','text',true),
('hosting_auto_deploy_origins','created_at','timestamp with time zone',true))
SELECT NOT EXISTS(SELECT 1 FROM required r LEFT JOIN pg_attribute a ON a.attrelid=to_regclass(current_schema()||'.'||r.table_name) AND a.attname=r.column_name AND a.attnum>0 AND NOT a.attisdropped WHERE a.attname IS NULL OR a.atttypid<>to_regtype(r.type_name) OR a.attnotnull<>r.not_null)
 AND EXISTS(SELECT 1 FROM pg_constraint c WHERE c.conrelid=to_regclass(current_schema()||'.hosting_auto_deploy_controls') AND c.conname='hosting_auto_control_body' AND c.contype='c' AND c.convalidated AND translate(pg_get_expr(c.conbin,c.conrelid),E' \n\r\t()','')='COALESCEjsonb_typeofbody=''object''::textANDbody->>''version''::text=''1''::textANDoctet_lengthbody::text<=65536,false')
 AND EXISTS(SELECT 1 FROM pg_constraint c WHERE c.conrelid=to_regclass(current_schema()||'.hosting_auto_deploy_demands') AND c.conname='hosting_auto_demand_body' AND c.contype='c' AND c.convalidated AND translate(pg_get_expr(c.conbin,c.conrelid),E' \n\r\t()','')='COALESCEjsonb_typeofbody=''object''::textANDbody->>''version''::text=''1''::textANDoctet_lengthbody::text<=131072,false')
 AND EXISTS(SELECT 1 FROM pg_constraint c WHERE c.conrelid=to_regclass(current_schema()||'.hosting_auto_deploy_origins') AND c.conname='hosting_auto_origin_body' AND c.contype='c' AND c.convalidated AND translate(pg_get_expr(c.conbin,c.conrelid),E' \n\r\t()','')='COALESCEjsonb_typeofbody=''object''::textANDbody->>''version''::text=''1''::textANDoctet_lengthbody::text<=131072,false')
 AND EXISTS(SELECT 1 FROM pg_constraint c WHERE c.conrelid=to_regclass(current_schema()||'.hosting_auto_deploy_demands') AND c.conname='hosting_auto_demand_stage' AND c.contype='c' AND c.convalidated AND translate(pg_get_expr(c.conbin,c.conrelid),E' \n\r\t()','')='stage=ANYARRAY[''waiting_build''::text,''waiting_deployment''::text,''waiting_switch''::text,''succeeded''::text,''failed''::text,''blocked''::text,''expired''::text]')
 AND EXISTS(SELECT 1 FROM pg_constraint c WHERE c.conrelid=to_regclass(current_schema()||'.hosting_auto_deploy_demands') AND c.conname='hosting_auto_demand_deadline' AND c.contype='c' AND c.convalidated AND translate(pg_get_expr(c.conbin,c.conrelid),E' \n\r\t()','')='expires_at>created_atANDexpires_at<=created_at+''24:00:00''::interval')
 AND EXISTS(SELECT 1 FROM pg_constraint c WHERE c.conrelid=to_regclass(current_schema()||'.hosting_auto_deploy_origins') AND c.conname='hosting_auto_origin_kind' AND c.contype='c' AND c.convalidated AND translate(pg_get_expr(c.conbin,c.conrelid),E' \n\r\t()','')='kind=''deployment''::textANDdeployment_operation_idISNOTNULLANDdeployment_operation_id=operation_idANDproxy_operation_idISNULLORkind=''switch''::textANDproxy_operation_idISNOTNULLANDproxy_operation_id=operation_idANDdeployment_operation_idISNULL')
 AND EXISTS(SELECT 1 FROM pg_constraint c WHERE c.conrelid=to_regclass(current_schema()||'.hosting_auto_deploy_controls') AND c.conname='hosting_auto_deploy_controls_policy_generation_check' AND c.contype='c' AND c.convalidated AND translate(pg_get_expr(c.conbin,c.conrelid),E' \n\r\t()','')='policy_generation>0')
 AND EXISTS(SELECT 1 FROM pg_constraint c WHERE c.conrelid=to_regclass(current_schema()||'.hosting_auto_deploy_controls') AND c.conname='hosting_auto_deploy_controls_manual_epoch_check' AND c.contype='c' AND c.convalidated AND translate(pg_get_expr(c.conbin,c.conrelid),E' \n\r\t()','')='manual_epoch>=0')
 AND EXISTS(SELECT 1 FROM pg_constraint c WHERE c.conrelid=to_regclass(current_schema()||'.hosting_auto_deploy_demands') AND c.conname='hosting_auto_deploy_demands_policy_generation_check' AND c.contype='c' AND c.convalidated AND translate(pg_get_expr(c.conbin,c.conrelid),E' \n\r\t()','')='policy_generation>0')
 AND EXISTS(SELECT 1 FROM pg_constraint c WHERE c.conrelid=to_regclass(current_schema()||'.hosting_auto_deploy_demands') AND c.conname='hosting_auto_deploy_demands_attempts_check' AND c.contype='c' AND c.convalidated AND translate(pg_get_expr(c.conbin,c.conrelid),E' \n\r\t()','')='attempts>=0ANDattempts<=32')
 AND EXISTS(SELECT 1 FROM pg_constraint c WHERE c.conrelid=to_regclass(current_schema()||'.hosting_auto_deploy_controls') AND c.contype='p' AND c.convalidated AND ARRAY(SELECT a.attname::text FROM unnest(c.conkey) WITH ORDINALITY k(attnum,n) JOIN pg_attribute a ON a.attrelid=c.conrelid AND a.attnum=k.attnum ORDER BY k.n)=ARRAY['resource_id']::text[])
 AND EXISTS(SELECT 1 FROM pg_constraint c WHERE c.conrelid=to_regclass(current_schema()||'.hosting_auto_deploy_demands') AND c.contype='p' AND c.convalidated AND ARRAY(SELECT a.attname::text FROM unnest(c.conkey) WITH ORDINALITY k(attnum,n) JOIN pg_attribute a ON a.attrelid=c.conrelid AND a.attnum=k.attnum ORDER BY k.n)=ARRAY['resource_id']::text[])
 AND EXISTS(SELECT 1 FROM pg_constraint c WHERE c.conrelid=to_regclass(current_schema()||'.hosting_auto_deploy_demands') AND c.contype='u' AND c.convalidated AND ARRAY(SELECT a.attname::text FROM unnest(c.conkey) WITH ORDINALITY k(attnum,n) JOIN pg_attribute a ON a.attrelid=c.conrelid AND a.attnum=k.attnum ORDER BY k.n)=ARRAY['id']::text[])
 AND EXISTS(SELECT 1 FROM pg_constraint c WHERE c.conrelid=to_regclass(current_schema()||'.hosting_auto_deploy_origins') AND c.contype='p' AND c.convalidated AND ARRAY(SELECT a.attname::text FROM unnest(c.conkey) WITH ORDINALITY k(attnum,n) JOIN pg_attribute a ON a.attrelid=c.conrelid AND a.attnum=k.attnum ORDER BY k.n)=ARRAY['operation_id']::text[])
 AND EXISTS(SELECT 1 FROM pg_constraint c WHERE c.conrelid=to_regclass(current_schema()||'.hosting_auto_deploy_origins') AND c.contype='u' AND c.convalidated AND ARRAY(SELECT a.attname::text FROM unnest(c.conkey) WITH ORDINALITY k(attnum,n) JOIN pg_attribute a ON a.attrelid=c.conrelid AND a.attnum=k.attnum ORDER BY k.n)=ARRAY['deployment_operation_id']::text[])
 AND EXISTS(SELECT 1 FROM pg_constraint c WHERE c.conrelid=to_regclass(current_schema()||'.hosting_auto_deploy_origins') AND c.contype='u' AND c.convalidated AND ARRAY(SELECT a.attname::text FROM unnest(c.conkey) WITH ORDINALITY k(attnum,n) JOIN pg_attribute a ON a.attrelid=c.conrelid AND a.attnum=k.attnum ORDER BY k.n)=ARRAY['proxy_operation_id']::text[])
 AND EXISTS(SELECT 1 FROM pg_constraint c JOIN pg_attribute a ON a.attrelid=c.conrelid AND c.conkey=ARRAY[a.attnum] JOIN pg_attribute target ON target.attrelid=c.confrelid AND c.confkey=ARRAY[target.attnum] WHERE c.conrelid=to_regclass(current_schema()||'.hosting_auto_deploy_controls') AND a.attname='resource_id' AND c.contype='f' AND c.convalidated AND c.confrelid=to_regclass(current_schema()||'.resources') AND c.confdeltype='r' AND target.attname='id')
 AND EXISTS(SELECT 1 FROM pg_constraint c JOIN pg_attribute a ON a.attrelid=c.conrelid AND c.conkey=ARRAY[a.attnum] JOIN pg_attribute target ON target.attrelid=c.confrelid AND c.confkey=ARRAY[target.attnum] WHERE c.conrelid=to_regclass(current_schema()||'.hosting_auto_deploy_controls') AND a.attname='policy_id' AND c.contype='f' AND c.convalidated AND c.confrelid=to_regclass(current_schema()||'.hosting_github_push_policies') AND c.confdeltype='r' AND target.attname='id')
 AND EXISTS(SELECT 1 FROM pg_constraint c JOIN pg_attribute a ON a.attrelid=c.conrelid AND c.conkey=ARRAY[a.attnum] JOIN pg_attribute target ON target.attrelid=c.confrelid AND c.confkey=ARRAY[target.attnum] WHERE c.conrelid=to_regclass(current_schema()||'.hosting_auto_deploy_demands') AND a.attname='resource_id' AND c.contype='f' AND c.convalidated AND c.confrelid=to_regclass(current_schema()||'.resources') AND c.confdeltype='r' AND target.attname='id')
 AND EXISTS(SELECT 1 FROM pg_constraint c JOIN pg_attribute a ON a.attrelid=c.conrelid AND c.conkey=ARRAY[a.attnum] JOIN pg_attribute target ON target.attrelid=c.confrelid AND c.confkey=ARRAY[target.attnum] WHERE c.conrelid=to_regclass(current_schema()||'.hosting_auto_deploy_demands') AND a.attname='policy_id' AND c.contype='f' AND c.convalidated AND c.confrelid=to_regclass(current_schema()||'.hosting_github_push_policies') AND c.confdeltype='r' AND target.attname='id')
 AND EXISTS(SELECT 1 FROM pg_constraint c JOIN pg_attribute a ON a.attrelid=c.conrelid AND c.conkey=ARRAY[a.attnum] JOIN pg_attribute target ON target.attrelid=c.confrelid AND c.confkey=ARRAY[target.attnum] WHERE c.conrelid=to_regclass(current_schema()||'.hosting_auto_deploy_demands') AND a.attname='build_id' AND c.contype='f' AND c.convalidated AND c.confrelid=to_regclass(current_schema()||'.hosting_builds') AND c.confdeltype='r' AND target.attname='id')
 AND EXISTS(SELECT 1 FROM pg_constraint c JOIN pg_attribute a ON a.attrelid=c.conrelid AND c.conkey=ARRAY[a.attnum] JOIN pg_attribute target ON target.attrelid=c.confrelid AND c.confkey=ARRAY[target.attnum] WHERE c.conrelid=to_regclass(current_schema()||'.hosting_auto_deploy_origins') AND a.attname='resource_id' AND c.contype='f' AND c.convalidated AND c.confrelid=to_regclass(current_schema()||'.resources') AND c.confdeltype='r' AND target.attname='id')
 AND EXISTS(SELECT 1 FROM pg_constraint c JOIN pg_attribute a ON a.attrelid=c.conrelid AND c.conkey=ARRAY[a.attnum] JOIN pg_attribute target ON target.attrelid=c.confrelid AND c.confkey=ARRAY[target.attnum] WHERE c.conrelid=to_regclass(current_schema()||'.hosting_auto_deploy_origins') AND a.attname='build_id' AND c.contype='f' AND c.convalidated AND c.confrelid=to_regclass(current_schema()||'.hosting_builds') AND c.confdeltype='r' AND target.attname='id')
 AND EXISTS(SELECT 1 FROM pg_constraint c JOIN pg_attribute a ON a.attrelid=c.conrelid AND c.conkey=ARRAY[a.attnum] JOIN pg_attribute target ON target.attrelid=c.confrelid AND c.confkey=ARRAY[target.attnum] WHERE c.conrelid=to_regclass(current_schema()||'.hosting_auto_deploy_origins') AND a.attname='deployment_operation_id' AND c.contype='f' AND c.convalidated AND c.confrelid=to_regclass(current_schema()||'.hosting_operations') AND c.confdeltype='r' AND target.attname='id')
 AND EXISTS(SELECT 1 FROM pg_constraint c JOIN pg_attribute a ON a.attrelid=c.conrelid AND c.conkey=ARRAY[a.attnum] JOIN pg_attribute target ON target.attrelid=c.confrelid AND c.confkey=ARRAY[target.attnum] WHERE c.conrelid=to_regclass(current_schema()||'.hosting_auto_deploy_origins') AND a.attname='proxy_operation_id' AND c.contype='f' AND c.convalidated AND c.confrelid=to_regclass(current_schema()||'.hosting_proxy_operations') AND c.confdeltype='r' AND target.attname='id')
 AND EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass(current_schema()||'.hosting_auto_deploy_origins') AND tgname='hosting_auto_deploy_origin_immutable' AND tgtype=27 AND tgenabled='O' AND tgfoid=to_regprocedure(current_schema()||'.nerocd_hosting_execution_immutable()'))
 AND EXISTS(SELECT 1 FROM pg_index WHERE indexrelid=to_regclass(current_schema()||'.hosting_auto_deploy_due') AND indisvalid AND indisready)
 AND EXISTS(SELECT 1 FROM pg_index WHERE indexrelid=to_regclass(current_schema()||'.hosting_auto_deploy_origin_resource') AND indisvalid AND indisready);
$$;
DO $$ BEGIN
 EXECUTE format('ALTER FUNCTION %I.nerocd_hosting_auto_deploy_schema_compatible() SET search_path TO %I, pg_catalog, pg_temp',current_schema(),current_schema());
END $$;
