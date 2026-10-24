-- Pending desired bindings do not authorize network effects or secret delivery.
CREATE TABLE hosting_database_bindings (
 id text PRIMARY KEY,
 application_resource_id text NOT NULL UNIQUE,
 application_binding_id text NOT NULL UNIQUE,
 database_resource_id text NOT NULL REFERENCES hosting_databases(resource_id),
 database_binding_id text NOT NULL,
 project_id text NOT NULL,
 destination_id text NOT NULL REFERENCES destinations(id),
 database_operation_id text NOT NULL REFERENCES hosting_operations(id),
 runtime_generation bigint NOT NULL CHECK(runtime_generation>0),
 runtime_hash text NOT NULL CHECK(runtime_hash~'^sha256:[0-9a-f]{64}$'),
 credential_secret_id text NOT NULL,
 credential_version bigint NOT NULL CHECK(credential_version>0),
 body jsonb NOT NULL CHECK(jsonb_typeof(body)='object' AND octet_length(body::text)<=16384),
 body_hash text NOT NULL CHECK(body_hash~'^sha256:[0-9a-f]{64}$'),
 created_by text NOT NULL REFERENCES principals(id),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(application_resource_id,project_id) REFERENCES resources(id,project_id),
 FOREIGN KEY(database_resource_id,project_id) REFERENCES resources(id,project_id),
 FOREIGN KEY(application_binding_id,application_resource_id,destination_id) REFERENCES resource_bindings(id,resource_id,destination_id),
 FOREIGN KEY(database_binding_id,database_resource_id,destination_id) REFERENCES resource_bindings(id,resource_id,destination_id),
 FOREIGN KEY(application_resource_id,project_id,runtime_generation) REFERENCES hosting_runtime_configuration_versions(resource_id,project_id,generation),
 FOREIGN KEY(credential_secret_id,project_id) REFERENCES project_managed_secrets(id,project_id),
 FOREIGN KEY(credential_secret_id,credential_version) REFERENCES project_managed_secret_versions(secret_id,version),
 CHECK(application_resource_id<>database_resource_id AND application_binding_id<>database_binding_id)
);
CREATE TABLE hosting_database_binding_receipts (
 application_resource_id text NOT NULL REFERENCES hosting_database_bindings(application_resource_id) DEFERRABLE INITIALLY DEFERRED,
 request_id text NOT NULL CHECK(request_id~'^[0-9a-f]{32}$'),
 actor_id text NOT NULL REFERENCES principals(id),
 accepted_request jsonb NOT NULL CHECK(jsonb_typeof(accepted_request)='object' AND octet_length(accepted_request::text)<=4096),
 request_hash text NOT NULL CHECK(request_hash~'^sha256:[0-9a-f]{64}$'),
 binding_hash text NOT NULL CHECK(binding_hash~'^sha256:[0-9a-f]{64}$'),
 runtime_generation bigint NOT NULL,
 runtime_hash text NOT NULL,
 audit_id text NOT NULL UNIQUE REFERENCES audit_events(id) DEFERRABLE INITIALLY DEFERRED,
 PRIMARY KEY(application_resource_id,request_id),
 UNIQUE(application_resource_id),
 FOREIGN KEY(application_resource_id,runtime_generation) REFERENCES hosting_runtime_configuration_versions(resource_id,generation)
);
CREATE TRIGGER hosting_database_bindings_immutable BEFORE UPDATE OR DELETE ON hosting_database_bindings FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_runtime_immutable();
CREATE TRIGGER hosting_database_binding_receipts_immutable BEFORE UPDATE OR DELETE ON hosting_database_binding_receipts FOR EACH ROW EXECUTE FUNCTION nerocd_hosting_runtime_immutable();

-- Match the fixed Go DTO field order. All strings are validated closed identities;
-- no plaintext runtime values pass through this canonical binding serializer.
CREATE FUNCTION nerocd_database_binding_hash(doc jsonb) RETURNS text LANGUAGE plpgsql IMMUTABLE AS $$
DECLARE result text:='{'; key text; value text; nested text; child text;
BEGIN
 FOREACH key IN ARRAY ARRAY['version','id','generation','application_resource_id','application_resource_generation','application_binding_id','database_resource_id','database_resource_generation','database_binding_id','project_id','destination_id','server_id','runner_id','database','credential','network_name','alias','environment_names','runtime_configuration','hash'] LOOP
  IF key='hash' THEN value:='""';
  ELSIF key IN ('database','credential','runtime_configuration') THEN
   nested:='{';
   FOREACH child IN ARRAY CASE key WHEN 'database' THEN ARRAY['engine','recipe_version','configuration_hash'] WHEN 'credential' THEN ARRAY['role','username','secret_id','version'] ELSE ARRAY['generation','hash'] END LOOP
    nested:=nested||to_jsonb(child)::text||':'||(doc->key->child)::text||',';
   END LOOP;
   value:=left(nested,length(nested)-1)||'}';
  ELSIF key='environment_names' THEN
   SELECT '['||string_agg(entry::text,',' ORDER BY ordinal)||']' INTO value FROM jsonb_array_elements(doc->key) WITH ORDINALITY AS e(entry,ordinal);
  ELSE value:=(doc->key)::text;
  END IF;
  result:=result||to_jsonb(key)::text||':'||value||',';
 END LOOP;
 RETURN 'sha256:'||encode(sha256(convert_to(left(result,length(result)-1)||'}','UTF8')),'hex');
END $$;

CREATE FUNCTION nerocd_database_binding_request_hash(actor text,application text,request jsonb) RETURNS text LANGUAGE sql IMMUTABLE AS $$
 SELECT 'sha256:'||encode(sha256(convert_to('{"Actor":'||to_jsonb(actor)::text||',"Application":'||to_jsonb(application)::text||',"Input":{"database_resource_id":'||(request->'database_resource_id')::text||',"expected_resource_generation":'||(request->'expected_resource_generation')::text||',"expected_runtime_generation":'||(request->'expected_runtime_generation')::text||',"expected_database_generation":'||(request->'expected_database_generation')::text||',"request_id":'||(request->'request_id')::text||'}}','UTF8')),'hex');
$$;

CREATE FUNCTION nerocd_database_binding_consistent() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE b hosting_database_bindings; receipt hosting_database_binding_receipts; app resources; db resources; target resource_bindings; d hosting_databases; credential hosting_database_credentials; runtime hosting_runtime_configuration_versions; prior jsonb; plan jsonb; healthy jsonb; expected jsonb; names jsonb; generated jsonb; entry jsonb; runtime_receipt hosting_runtime_configuration_receipts;
BEGIN
 SELECT * INTO STRICT b FROM hosting_database_bindings WHERE application_resource_id=NEW.application_resource_id;
 SELECT * INTO STRICT receipt FROM hosting_database_binding_receipts WHERE application_resource_id=b.application_resource_id;
 SELECT * INTO STRICT app FROM resources WHERE id=b.application_resource_id;
 SELECT * INTO STRICT db FROM resources WHERE id=b.database_resource_id;
 SELECT * INTO STRICT d FROM hosting_databases WHERE resource_id=db.id;
 SELECT * INTO STRICT target FROM resource_bindings WHERE id=b.application_binding_id;
 SELECT * INTO STRICT credential FROM hosting_database_credentials WHERE resource_id=db.id AND role='app';
 SELECT * INTO STRICT runtime FROM hosting_runtime_configuration_versions WHERE resource_id=app.id AND generation=b.runtime_generation;
 SELECT * INTO STRICT runtime_receipt FROM hosting_runtime_configuration_receipts WHERE resource_id=app.id AND runtime_generation=b.runtime_generation;
 SELECT body INTO STRICT plan FROM hosting_apply_plans WHERE operation_id=b.database_operation_id;
 SELECT bs.healthy INTO STRICT healthy FROM hosting_binding_state bs WHERE bs.binding_id=b.database_binding_id;
 IF app.kind<>'application' OR app.source->>'kind'<>'image' OR EXISTS(SELECT 1 FROM hosting_storage WHERE resource_id=app.id) OR d.binding_id<>b.database_binding_id OR d.destination_id<>b.destination_id OR credential.secret_id<>b.credential_secret_id OR credential.version<>b.credential_version OR credential.username<>'ncd_app' OR runtime.body_hash<>b.runtime_hash OR runtime.project_id<>b.project_id OR runtime.created_by<>b.created_by OR NOT EXISTS(SELECT 1 FROM hosting_runtime_configuration_versions v WHERE v.resource_id=app.id AND v.generation=b.runtime_generation AND v.xmin::text::bigint=mod(txid_current(),4294967296)) THEN RAISE EXCEPTION 'database binding target/runtime mismatch' USING ERRCODE='23514'; END IF;
 IF plan->'snapshot'->>'resource_id' IS DISTINCT FROM db.id OR plan->'snapshot'->>'binding_id' IS DISTINCT FROM b.database_binding_id OR (plan->'snapshot'->>'resource_generation')::bigint<>db.generation OR plan->>'compiler_version'<>'database-hosting.private.owned-storage.v1' OR plan->'database'->'reference' IS DISTINCT FROM plan->'snapshot'->'database' OR healthy->>'operation_id' IS DISTINCT FROM b.database_operation_id OR healthy->>'plan_hash' IS DISTINCT FROM plan->>'hash' OR healthy->'snapshot' IS DISTINCT FROM plan->'snapshot' OR healthy->'release' IS DISTINCT FROM plan->'release' OR NOT EXISTS(SELECT 1 FROM hosting_operations o JOIN hosting_receipts r ON r.operation_id=o.id AND r.phase='complete' WHERE o.id=b.database_operation_id AND o.resource_id=db.id AND o.project_id=b.project_id AND o.state='succeeded' AND r.body->>'ready'='true' AND r.body->>'container_id'=healthy->>'container_id' AND r.body->>'plan_hash'=plan->>'hash') OR NOT EXISTS(SELECT 1 FROM hosting_storage_materializations m WHERE m.storage_id=d.storage_id AND m.materialized_receipt_id IS NOT NULL) THEN RAISE EXCEPTION 'database binding requires durable healthy v1 proof' USING ERRCODE='23514'; END IF;
 IF NOT EXISTS(SELECT 1 FROM destination_project_grants WHERE destination_id=b.destination_id AND project_id=b.project_id AND revoked_at IS NULL) OR NOT EXISTS(SELECT 1 FROM projects WHERE id=b.project_id AND archived_at IS NULL) OR NOT EXISTS(SELECT 1 FROM project_managed_secrets WHERE id=b.credential_secret_id AND project_id=b.project_id AND status='active') OR EXISTS(SELECT 1 FROM hosting_operations WHERE resource_id IN(app.id,db.id) AND state NOT IN('succeeded','failed','canceled')) OR EXISTS(SELECT 1 FROM hosting_binding_state WHERE binding_id IN(b.application_binding_id,b.database_binding_id) AND (hold_operation_id IS NOT NULL OR NOT restore_admitted)) OR EXISTS(SELECT 1 FROM hosting_binding_restore_holds WHERE binding_id IN(b.application_binding_id,b.database_binding_id)) THEN RAISE EXCEPTION 'database binding authority target is held' USING ERRCODE='23514'; END IF;
 IF d.engine='postgresql' THEN
  names:='["PGDATABASE","PGHOST","PGPASSWORD","PGPORT","PGUSER"]'::jsonb;
  generated:=jsonb_build_array(jsonb_build_object('name','PGDATABASE','kind','literal','value','app'),jsonb_build_object('name','PGHOST','kind','literal','value',b.body->>'alias'),jsonb_build_object('name','PGPASSWORD','kind','managed_secret','secret',jsonb_build_object('secret_id',credential.secret_id,'version',credential.version)),jsonb_build_object('name','PGPORT','kind','literal','value','5432'),jsonb_build_object('name','PGUSER','kind','literal','value','ncd_app'));
 ELSE
  names:='["REDIS_HOST","REDIS_PASSWORD","REDIS_PORT","REDIS_USERNAME"]'::jsonb;
  generated:=jsonb_build_array(jsonb_build_object('name','REDIS_HOST','kind','literal','value',b.body->>'alias'),jsonb_build_object('name','REDIS_PASSWORD','kind','managed_secret','secret',jsonb_build_object('secret_id',credential.secret_id,'version',credential.version)),jsonb_build_object('name','REDIS_PORT','kind','literal','value','6379'),jsonb_build_object('name','REDIS_USERNAME','kind','literal','value','ncd_app'));
 END IF;
 expected:=jsonb_build_object('version','database-binding.private.v1','id',b.id,'generation',1,'application_resource_id',app.id,'application_resource_generation',app.generation,'application_binding_id',b.application_binding_id,'database_resource_id',db.id,'database_resource_generation',db.generation,'database_binding_id',b.database_binding_id,'project_id',b.project_id,'destination_id',b.destination_id,'server_id',(SELECT server_id FROM destinations WHERE id=b.destination_id),'runner_id',(SELECT srv.runner_id FROM destinations dest JOIN servers srv ON srv.id=dest.server_id WHERE dest.id=b.destination_id),'database',plan->'snapshot'->'database','credential',jsonb_build_object('role','app','username',credential.username,'secret_id',credential.secret_id,'version',credential.version),'network_name',plan->'database'->>'network_name','alias','ncdb-'||left(encode(sha256(convert_to('{"Binding":'||to_jsonb(b.database_binding_id)::text||'}','UTF8')),'hex'),32),'environment_names',names,'runtime_configuration',jsonb_build_object('generation',b.runtime_generation,'hash',b.runtime_hash),'hash',b.body_hash);
 IF EXISTS(SELECT 1 FROM jsonb_each(b.body) e WHERE e.key IN('id','application_resource_id','application_binding_id','database_resource_id','database_binding_id','project_id','destination_id','server_id','runner_id') AND e.value#>>'{}' !~ '^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$') OR (b.body->'generation')::text !~ '^[1-9][0-9]*$' OR (b.body->'application_resource_generation')::text !~ '^[1-9][0-9]*$' OR (b.body->'database_resource_generation')::text !~ '^[1-9][0-9]*$' OR (b.body->'runtime_configuration'->'generation')::text !~ '^[1-9][0-9]*$' OR (b.body->'credential'->'version')::text !~ '^[1-9][0-9]*$' OR b.body IS DISTINCT FROM expected OR b.body_hash IS DISTINCT FROM nerocd_database_binding_hash(b.body) OR b.id IS DISTINCT FROM 'hdb_'||left(encode(sha256(convert_to('{"Application":'||to_jsonb(b.application_binding_id)::text||',"Database":'||to_jsonb(b.database_binding_id)::text||'}','UTF8')),'hex'),32) THEN RAISE EXCEPTION 'database binding canonical document mismatch' USING ERRCODE='23514'; END IF;
 IF EXISTS(SELECT 1 FROM jsonb_each(receipt.accepted_request) e WHERE e.key IN('expected_resource_generation','expected_runtime_generation','expected_database_generation') AND e.value::text !~ '^(0|[1-9][0-9]*)$') OR receipt.request_hash IS DISTINCT FROM nerocd_database_binding_request_hash(receipt.actor_id,app.id,receipt.accepted_request) OR receipt.actor_id<>b.created_by OR receipt.binding_hash<>b.body_hash OR receipt.runtime_generation<>b.runtime_generation OR receipt.runtime_hash<>b.runtime_hash OR receipt.accepted_request IS DISTINCT FROM jsonb_build_object('database_resource_id',db.id,'expected_resource_generation',app.generation,'expected_runtime_generation',b.runtime_generation-1,'expected_database_generation',db.generation,'request_id',receipt.request_id) OR runtime_receipt.actor_id<>receipt.actor_id OR runtime_receipt.audit_id<>receipt.audit_id OR runtime_receipt.request_id<>receipt.request_id OR runtime_receipt.request_hash<>receipt.request_hash OR runtime_receipt.expected_resource_generation<>app.generation OR runtime_receipt.expected_runtime_generation<>b.runtime_generation-1 THEN RAISE EXCEPTION 'database binding accepted receipt mismatch' USING ERRCODE='23514'; END IF;
 IF NOT EXISTS(SELECT 1 FROM audit_events a WHERE a.id=receipt.audit_id AND a.actor_id=b.created_by AND a.target_id=app.id AND a.action='hosting.runtime_configuration.updated' AND a.metadata->>'purpose'='database_binding_pending' AND a.metadata->>'binding_id'=b.id AND a.metadata->>'database_resource_id'=db.id) THEN RAISE EXCEPTION 'database binding audit mismatch' USING ERRCODE='23514'; END IF;
 IF b.runtime_generation=1 THEN prior:='[]'::jsonb; ELSE SELECT body->'entries' INTO STRICT prior FROM hosting_runtime_configuration_versions WHERE resource_id=app.id AND generation=b.runtime_generation-1; END IF;
 IF EXISTS(SELECT 1 FROM jsonb_array_elements(prior) e WHERE names ? (e->>'name')) OR EXISTS((SELECT value FROM jsonb_array_elements(runtime.body->'entries') EXCEPT SELECT value FROM jsonb_array_elements(prior||generated)) UNION ALL (SELECT value FROM jsonb_array_elements(prior||generated) EXCEPT SELECT value FROM jsonb_array_elements(runtime.body->'entries'))) THEN RAISE EXCEPTION 'database binding generated entries mismatch' USING ERRCODE='23514'; END IF;
 RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER hosting_database_binding_consistency AFTER INSERT ON hosting_database_bindings DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION nerocd_database_binding_consistent();
CREATE CONSTRAINT TRIGGER hosting_database_binding_receipt_consistency AFTER INSERT ON hosting_database_binding_receipts DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION nerocd_database_binding_consistent();

-- Existing rows and exact ACK replays remain readable. Only new effects are denied.
CREATE FUNCTION nerocd_database_binding_pending_effect() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE resource_key text;
BEGIN
 IF TG_TABLE_NAME='hosting_runtime_configuration_versions' THEN
  resource_key:=NEW.resource_id;
  IF TG_WHEN='AFTER' AND EXISTS(SELECT 1 FROM hosting_database_bindings b WHERE b.application_resource_id=resource_key AND b.runtime_generation=NEW.generation AND b.xmin::text::bigint=mod(txid_current(),4294967296)) THEN RETURN NEW; END IF;
 ELSIF TG_TABLE_NAME='hosting_operations' THEN resource_key:=NEW.resource_id;
 ELSIF TG_TABLE_NAME='run_leases' THEN SELECT o.resource_id INTO resource_key FROM hosting_task_targets t JOIN hosting_operations o ON o.id=t.operation_id WHERE t.run_id=NEW.run_id AND t.mode='deploy';
 ELSIF TG_TABLE_NAME='audit_events' THEN
  IF NEW.action<>'hosting.runtime_environment_authorized' THEN RETURN NEW; END IF;
  SELECT resource_id INTO resource_key FROM hosting_operations WHERE id=NEW.target_id;
 ELSE SELECT resource_id INTO resource_key FROM hosting_operations WHERE id=NEW.operation_id;
 END IF;
 IF EXISTS(SELECT 1 FROM hosting_database_bindings WHERE application_resource_id=resource_key) THEN RAISE EXCEPTION 'database binding connectivity is pending' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER hosting_binding_pending_runtime BEFORE INSERT ON hosting_runtime_configuration_versions FOR EACH ROW EXECUTE FUNCTION nerocd_database_binding_pending_effect();
CREATE TRIGGER hosting_binding_pending_operation BEFORE INSERT ON hosting_operations FOR EACH ROW EXECUTE FUNCTION nerocd_database_binding_pending_effect();
CREATE TRIGGER hosting_binding_pending_release BEFORE INSERT ON hosting_releases FOR EACH ROW EXECUTE FUNCTION nerocd_database_binding_pending_effect();
CREATE TRIGGER hosting_binding_pending_plan BEFORE INSERT ON hosting_apply_plans FOR EACH ROW EXECUTE FUNCTION nerocd_database_binding_pending_effect();
CREATE TRIGGER hosting_binding_pending_admission BEFORE INSERT ON hosting_apply_admissions FOR EACH ROW EXECUTE FUNCTION nerocd_database_binding_pending_effect();
CREATE TRIGGER hosting_binding_pending_target BEFORE INSERT ON hosting_task_targets FOR EACH ROW EXECUTE FUNCTION nerocd_database_binding_pending_effect();
CREATE TRIGGER hosting_binding_pending_lease BEFORE INSERT ON run_leases FOR EACH ROW EXECUTE FUNCTION nerocd_database_binding_pending_effect();
CREATE TRIGGER hosting_binding_pending_delivery BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION nerocd_database_binding_pending_effect();
CREATE CONSTRAINT TRIGGER hosting_runtime_configuration_versions_binding_pending_commit AFTER INSERT ON hosting_runtime_configuration_versions DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION nerocd_database_binding_pending_effect();
CREATE CONSTRAINT TRIGGER hosting_operations_binding_pending_commit AFTER INSERT ON hosting_operations DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION nerocd_database_binding_pending_effect();
CREATE CONSTRAINT TRIGGER hosting_releases_binding_pending_commit AFTER INSERT ON hosting_releases DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION nerocd_database_binding_pending_effect();
CREATE CONSTRAINT TRIGGER hosting_apply_plans_binding_pending_commit AFTER INSERT ON hosting_apply_plans DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION nerocd_database_binding_pending_effect();
CREATE CONSTRAINT TRIGGER hosting_apply_admissions_binding_pending_commit AFTER INSERT ON hosting_apply_admissions DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION nerocd_database_binding_pending_effect();
CREATE CONSTRAINT TRIGGER hosting_task_targets_binding_pending_commit AFTER INSERT ON hosting_task_targets DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION nerocd_database_binding_pending_effect();
CREATE CONSTRAINT TRIGGER run_leases_binding_pending_commit AFTER INSERT ON run_leases DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION nerocd_database_binding_pending_effect();
CREATE CONSTRAINT TRIGGER audit_events_binding_pending_commit AFTER INSERT ON audit_events DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION nerocd_database_binding_pending_effect();
CREATE FUNCTION nerocd_database_binding_flush_constraints() RETURNS void LANGUAGE plpgsql AS $$ BEGIN SET CONSTRAINTS ALL IMMEDIATE; END $$;
CREATE FUNCTION nerocd_database_binding_schema_compatible() RETURNS boolean LANGUAGE sql STABLE AS $$
SELECT to_regclass(current_schema()||'.hosting_database_binding_receipts') IS NOT NULL
AND NOT EXISTS(SELECT 1 FROM (VALUES
 ('hosting_database_bindings','hosting_database_bindings_immutable'),
 ('hosting_database_binding_receipts','hosting_database_binding_receipts_immutable'),
 ('hosting_database_bindings','hosting_database_binding_consistency'),
 ('hosting_database_binding_receipts','hosting_database_binding_receipt_consistency'),
 ('hosting_runtime_configuration_versions','hosting_binding_pending_runtime'),
 ('hosting_operations','hosting_binding_pending_operation'),
 ('hosting_releases','hosting_binding_pending_release'),
 ('hosting_apply_plans','hosting_binding_pending_plan'),
 ('hosting_apply_admissions','hosting_binding_pending_admission'),
 ('hosting_task_targets','hosting_binding_pending_target'),
 ('run_leases','hosting_binding_pending_lease'),
 ('audit_events','hosting_binding_pending_delivery'),
 ('hosting_operations','hosting_operations_binding_pending_commit'),
 ('hosting_releases','hosting_releases_binding_pending_commit'),
 ('hosting_apply_plans','hosting_apply_plans_binding_pending_commit'),
 ('hosting_apply_admissions','hosting_apply_admissions_binding_pending_commit'),
 ('hosting_task_targets','hosting_task_targets_binding_pending_commit'),
 ('run_leases','run_leases_binding_pending_commit'),
 ('audit_events','audit_events_binding_pending_commit'),
 ('hosting_runtime_configuration_versions','hosting_runtime_configuration_versions_binding_pending_commit')) required(table_name,trigger_name)
 WHERE NOT EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=to_regclass(current_schema()||'.'||table_name) AND tgname=trigger_name AND tgenabled IN('O','A')));
$$;
DO $$ DECLARE owner_schema text:=current_schema(); routine text; BEGIN
 FOREACH routine IN ARRAY ARRAY['nerocd_database_binding_request_hash(text,text,jsonb)','nerocd_database_binding_schema_compatible()','nerocd_database_binding_hash(jsonb)','nerocd_database_binding_consistent()','nerocd_database_binding_pending_effect()','nerocd_database_binding_flush_constraints()'] LOOP
  EXECUTE format('ALTER FUNCTION %I.%s SET search_path=%I,pg_catalog,pg_temp',owner_schema,routine,owner_schema);
 END LOOP;
END $$;
