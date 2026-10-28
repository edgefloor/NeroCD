-- Add the pinned native MySQL recipe to existing immutable database protocols.
ALTER TABLE hosting_databases DROP CONSTRAINT hosting_databases_check;
ALTER TABLE hosting_databases ADD CONSTRAINT hosting_databases_check CHECK(
 (engine='postgresql' AND recipe_version='postgresql-17.11-bookworm.v1' AND image_reference='docker.io/library/postgres@sha256:858bb5e98eed02eb8cafbcdc007f9a65d1b613972028944a8ec2133ccaf80d27') OR
 (engine='redis' AND recipe_version='redis-8.2.10-bookworm.v1' AND image_reference='docker.io/library/redis@sha256:e5f23377739f95a2f530b9c99f93accb4f634577f09d00a7d8e0638f2fdf69e0') OR
 (engine='mysql' AND recipe_version='mysql-8.4.13-oraclelinux9.v1' AND image_reference='docker.io/library/mysql@sha256:a0225d1ea6631c8bc2385dc4c2faba17bbf1834ecf369e1659a9a382bd8d5dd7'));
ALTER TABLE hosting_database_credentials DROP CONSTRAINT hosting_database_credentials_username_check;
ALTER TABLE hosting_database_credentials ADD CONSTRAINT hosting_database_credentials_username_check CHECK(username IN('postgres','ncd_app','ncd_probe','root'));


CREATE OR REPLACE FUNCTION nerocd_hosting_database_validate(resource_key text) RETURNS void LANGUAGE plpgsql AS $$
DECLARE d hosting_databases; r resources; b resource_bindings; s hosting_storage; receipt hosting_database_receipts; credential record; credential_count integer;
BEGIN
 SELECT * INTO STRICT d FROM hosting_databases WHERE resource_id=resource_key;
 SELECT * INTO STRICT r FROM resources WHERE id=d.resource_id;
 SELECT * INTO STRICT b FROM resource_bindings WHERE id=d.binding_id;
 SELECT * INTO STRICT s FROM hosting_storage WHERE id=d.storage_id;
 SELECT * INTO STRICT receipt FROM hosting_database_receipts WHERE resource_id=d.resource_id;
 IF EXISTS(SELECT 1 FROM jsonb_each(receipt.accepted_request) field WHERE jsonb_typeof(field.value)<>'string') THEN
  RAISE EXCEPTION 'database accepted request must contain string fields' USING ERRCODE='23514';
 END IF;
 IF r.kind<>'database' OR r.generation<>1 OR r.source IS DISTINCT FROM jsonb_build_object('kind','image','image',jsonb_build_object('reference',d.image_reference)) OR r.project_id<>d.project_id OR r.created_by<>d.created_by OR b.resource_id<>r.id OR b.destination_id<>d.destination_id OR b.created_by<>d.created_by OR s.resource_id<>r.id OR s.project_id<>d.project_id OR s.binding_id<>b.id OR s.destination_id<>d.destination_id OR s.created_by<>d.created_by OR s.mount_path<>(CASE d.engine WHEN 'postgresql' THEN '/var/lib/postgresql/data' WHEN 'mysql' THEN '/var/lib/mysql' ELSE '/data' END) OR receipt.project_id<>d.project_id OR receipt.actor_id<>d.created_by THEN
  RAISE EXCEPTION 'database connected metadata mismatch' USING ERRCODE='23514';
 END IF;
 IF receipt.accepted_request->>'project_id' IS DISTINCT FROM d.project_id OR receipt.accepted_request->>'request_id' IS DISTINCT FROM receipt.request_id OR receipt.accepted_request->>'project_environment_id' IS DISTINCT FROM r.project_environment_id OR receipt.accepted_request->>'name' IS DISTINCT FROM r.name OR receipt.accepted_request->>'destination_id' IS DISTINCT FROM d.destination_id OR receipt.accepted_request->>'engine' IS DISTINCT FROM d.engine OR receipt.accepted_request->>'recipe_version' IS DISTINCT FROM d.recipe_version OR (SELECT count(*) FROM jsonb_object_keys(receipt.accepted_request))<>7 THEN
  RAISE EXCEPTION 'database accepted request mismatch' USING ERRCODE='23514';
 END IF;
 PERFORM 1 FROM hosting_storage_receipts sr WHERE sr.storage_id=s.id AND sr.resource_id=r.id AND sr.actor_id=d.created_by AND sr.request_id=receipt.request_id AND sr.expected_resource_generation=1;
 IF NOT FOUND THEN RAISE EXCEPTION 'database storage receipt missing' USING ERRCODE='23514'; END IF;
 PERFORM 1 FROM destination_project_grants WHERE destination_id=d.destination_id AND project_id=d.project_id AND revoked_at IS NULL FOR SHARE;
 IF NOT FOUND THEN RAISE EXCEPTION 'database destination grant unavailable' USING ERRCODE='23514'; END IF;
 PERFORM 1 FROM audit_events a WHERE a.actor_id=d.created_by AND a.action='resource.created' AND a.target_id=r.id AND a.metadata->>'project_id'=d.project_id AND a.metadata->>'project_environment_id'=r.project_environment_id AND a.metadata->>'generation'='1';
 IF NOT FOUND THEN RAISE EXCEPTION 'database resource audit missing' USING ERRCODE='23514'; END IF;
 PERFORM 1 FROM audit_events a WHERE a.actor_id=d.created_by AND a.action='resource.destination_bound' AND a.target_id=r.id AND a.metadata->>'project_id'=d.project_id AND a.metadata->>'destination_id'=d.destination_id AND a.metadata->>'binding_id'=b.id;
 IF NOT FOUND THEN RAISE EXCEPTION 'database binding audit missing' USING ERRCODE='23514'; END IF;
 SELECT count(*) INTO credential_count FROM hosting_database_credentials WHERE resource_id=r.id;
 IF credential_count<>2 THEN RAISE EXCEPTION 'database requires two distinct credentials' USING ERRCODE='23514'; END IF;
 FOR credential IN SELECT c.*,m.project_id,m.current_version,m.status,m.created_by FROM hosting_database_credentials c JOIN project_managed_secrets m ON m.id=c.secret_id WHERE c.resource_id=r.id LOOP
  IF credential.project_id<>d.project_id OR credential.current_version<>1 OR credential.status<>'active' OR credential.created_by<>d.created_by OR NOT ((d.engine='postgresql' AND ((credential.role='admin' AND credential.username='postgres') OR (credential.role='app' AND credential.username='ncd_app'))) OR (d.engine='mysql' AND ((credential.role='admin' AND credential.username='root') OR (credential.role='app' AND credential.username='ncd_app'))) OR (d.engine='redis' AND ((credential.role='app' AND credential.username='ncd_app') OR (credential.role='probe' AND credential.username='ncd_probe')))) THEN
   RAISE EXCEPTION 'database credential scope mismatch' USING ERRCODE='23514';
  END IF;
  PERFORM 1 FROM project_managed_secret_versions v WHERE v.secret_id=credential.secret_id AND v.version=1 AND v.created_by=d.created_by;
  IF NOT FOUND THEN RAISE EXCEPTION 'database credential version missing' USING ERRCODE='23514'; END IF;
  PERFORM 1 FROM audit_events a WHERE a.actor_id=d.created_by AND a.action='managed_secret.created' AND a.target_id=credential.secret_id AND a.metadata->>'project_id'=d.project_id AND a.metadata->>'version'='1';
  IF NOT FOUND THEN RAISE EXCEPTION 'database credential audit missing' USING ERRCODE='23514'; END IF;
 END LOOP;
 PERFORM 1 FROM audit_events a WHERE a.id=receipt.audit_id AND a.actor_id=d.created_by AND a.action='hosting.database.created' AND a.target_id=r.id AND a.metadata->>'project_id'=d.project_id AND a.metadata->>'binding_id'=b.id AND a.metadata->>'storage_id'=s.id AND a.metadata->>'engine'=d.engine AND a.metadata->>'recipe_version'=d.recipe_version;
 IF NOT FOUND THEN RAISE EXCEPTION 'database creation audit mismatch' USING ERRCODE='23514'; END IF;
END;
$$;

CREATE OR REPLACE FUNCTION nerocd_hosting_database_execution_validate(operation_key text) RETURNS void LANGUAGE plpgsql AS $$
DECLARE m hosting_database_deployments; o hosting_operations; d hosting_databases; snapshot jsonb; expected_credentials jsonb;
BEGIN
 SELECT * INTO STRICT m FROM hosting_database_deployments WHERE operation_id=operation_key;
 SELECT * INTO STRICT o FROM hosting_operations WHERE id=operation_key;
 SELECT * INTO STRICT d FROM hosting_databases WHERE resource_id=m.resource_id;
 SELECT body INTO STRICT snapshot FROM hosting_snapshots WHERE operation_id=o.id;
 SELECT jsonb_agg(jsonb_build_object('role',role,'username',username,'secret_id',secret_id,'version',version) ORDER BY role) INTO expected_credentials FROM hosting_database_credentials WHERE resource_id=d.resource_id;
 IF o.resource_id<>d.resource_id OR o.project_id<>d.project_id OR o.binding_id<>d.binding_id OR m.binding_id<>d.binding_id OR m.destination_id<>d.destination_id OR m.credentials IS DISTINCT FROM expected_credentials OR m.reference->>'engine' IS DISTINCT FROM d.engine OR m.reference->>'recipe_version' IS DISTINCT FROM d.recipe_version OR (m.reference->>'configuration_hash' ~ '^sha256:[0-9a-f]{64}$') IS DISTINCT FROM true OR (SELECT count(*) FROM jsonb_object_keys(m.reference))<>3 THEN
  RAISE EXCEPTION 'database operation scope or references mismatch' USING ERRCODE='23514';
 END IF;
 IF snapshot->'database' IS DISTINCT FROM m.reference OR snapshot->>'policy_version' IS DISTINCT FROM 'database.private.owned-storage.v1' OR snapshot->>'resource_id' IS DISTINCT FROM d.resource_id OR snapshot->>'binding_id' IS DISTINCT FROM d.binding_id OR snapshot->>'destination_id' IS DISTINCT FROM d.destination_id OR snapshot->>'image_reference' IS DISTINCT FROM d.image_reference OR snapshot->'host_port' IS DISTINCT FROM '0'::jsonb OR snapshot->'container_port' IS DISTINCT FROM to_jsonb(CASE d.engine WHEN 'postgresql' THEN 5432 WHEN 'mysql' THEN 3306 ELSE 6379 END) OR snapshot ?| ARRAY['local_build','runtime_configuration','storage_route_continuity'] OR snapshot->'readiness' IS DISTINCT FROM '{"path":"","timeout_seconds":0}'::jsonb THEN
  RAISE EXCEPTION 'database snapshot family mismatch' USING ERRCODE='23514';
 END IF;
 PERFORM 1 FROM resources r JOIN resource_bindings b ON b.resource_id=r.id JOIN destinations dest ON dest.id=b.destination_id JOIN servers srv ON srv.id=dest.server_id JOIN hosting_storage storage ON storage.id=d.storage_id
 WHERE r.id=d.resource_id AND r.kind='database' AND r.generation=(snapshot->>'resource_generation')::bigint AND b.id=d.binding_id AND b.destination_id=d.destination_id AND snapshot->>'runtime_namespace'=b.runtime_namespace AND snapshot->>'server_id'=srv.id AND snapshot->>'runner_id'=srv.runner_id AND snapshot->'storage'=jsonb_build_object('storage_id',storage.id,'generation',storage.generation,'hash',storage.body_hash);
 IF NOT FOUND THEN RAISE EXCEPTION 'database snapshot target or storage mismatch' USING ERRCODE='23514'; END IF;
 IF EXISTS(SELECT 1 FROM hosting_release_claims WHERE operation_id=o.id) OR EXISTS(SELECT 1 FROM hosting_release_slots WHERE binding_id=o.binding_id) THEN
  RAISE EXCEPTION 'database cannot own a public release port' USING ERRCODE='23514';
 END IF;
END;
$$;

CREATE OR REPLACE FUNCTION nerocd_database_binding_consistent() RETURNS trigger LANGUAGE plpgsql AS $$
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
 ELSIF d.engine='mysql' THEN
  names:='["MYSQL_DATABASE","MYSQL_HOST","MYSQL_PASSWORD","MYSQL_PORT","MYSQL_USER"]'::jsonb;
  generated:=jsonb_build_array(jsonb_build_object('name','MYSQL_DATABASE','kind','literal','value','app'),jsonb_build_object('name','MYSQL_HOST','kind','literal','value',b.body->>'alias'),jsonb_build_object('name','MYSQL_PASSWORD','kind','managed_secret','secret',jsonb_build_object('secret_id',credential.secret_id,'version',credential.version)),jsonb_build_object('name','MYSQL_PORT','kind','literal','value','3306'),jsonb_build_object('name','MYSQL_USER','kind','literal','value','ncd_app'));
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

CREATE OR REPLACE FUNCTION nerocd_database_backup_admission() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE original hosting_database_backups; child hosting_operations; source hosting_operations; destination_key text;
BEGIN
 -- Use the existing destination admission lock before the stable role binding
 -- row. Both role admissions serialize even when issued as direct SQL. The
 -- following reads therefore see a competing admission after its commit.
 FOR destination_key IN SELECT DISTINCT destination_id FROM resource_bindings WHERE id IN (NEW.source_binding_id,NEW.target_binding_id) ORDER BY destination_id LOOP
  PERFORM pg_advisory_xact_lock(hashtextextended('nerocd-hosting-destination:'||destination_key,0));
 END LOOP;
 PERFORM 1 FROM hosting_binding_state WHERE binding_id=CASE WHEN NEW.mode='backup' THEN NEW.source_binding_id ELSE NEW.target_binding_id END FOR UPDATE;
 IF NOT FOUND THEN RAISE EXCEPTION 'database backup role binding unavailable' USING ERRCODE='23514'; END IF;
 IF (NEW.mode='backup' AND EXISTS(SELECT 1 FROM hosting_database_backups b WHERE b.ownership_held AND b.target_binding_id=NEW.source_binding_id)) OR
    (NEW.mode='restore' AND EXISTS(SELECT 1 FROM hosting_database_backups b WHERE b.ownership_held AND b.mode='backup' AND b.source_binding_id=NEW.target_binding_id)) THEN
  RAISE EXCEPTION 'database backup binding role already owned' USING ERRCODE='23514';
 END IF;
 SELECT * INTO source FROM hosting_operations WHERE id=NEW.plan->'source'->>'operation_id';
 IF source.id IS NULL OR source.resource_id<>NEW.source_resource_id OR source.binding_id<>NEW.source_binding_id OR source.project_id<>NEW.project_id OR NEW.plan->>'actor_id' IS DISTINCT FROM NEW.created_by OR NEW.plan->>'runner_id' IS DISTINCT FROM NEW.runner_id OR NEW.plan->'operation'->>'id' IS DISTINCT FROM NEW.id OR NEW.plan->'operation'->>'backup_id' IS DISTINCT FROM NEW.backup_id OR NEW.plan->'operation'->>'mode' IS DISTINCT FROM NEW.mode OR NEW.plan->'source'->'snapshot'->>'resource_generation' IS DISTINCT FROM NEW.source_generation::text OR NEW.plan->>'location_id' IS DISTINCT FROM NEW.backup_id OR NEW.plan->>'hash' IS DISTINCT FROM NEW.plan_hash OR NEW.run_id IS NOT NULL OR NOT NEW.ownership_held OR NOT EXISTS(SELECT 1 FROM hosting_apply_plans p WHERE p.operation_id=source.id AND p.body=NEW.plan->'source_plan' AND p.body_hash=NEW.plan->'source'->>'plan_hash') THEN RAISE EXCEPTION 'database backup admission identity mismatch' USING ERRCODE='23514'; END IF;
 IF NEW.mode='backup' THEN
  IF NEW.state<>'queued' OR NEW.blob IS NOT NULL OR NOT EXISTS(SELECT 1 FROM hosting_binding_state b WHERE b.binding_id=NEW.source_binding_id AND b.healthy=NEW.plan->'source' AND b.hold_operation_id IS NULL AND b.restore_admitted) THEN RAISE EXCEPTION 'database backup source is not settled' USING ERRCODE='23514'; END IF;
 ELSE
  SELECT * INTO original FROM hosting_database_backups WHERE id=NEW.backup_id;
  SELECT * INTO child FROM hosting_operations WHERE id=NEW.deployment_operation_id;
  IF original.id IS NULL OR original.mode<>'backup' OR original.state<>'succeeded' OR original.ownership_held OR original.blob IS DISTINCT FROM NEW.blob OR original.project_id<>NEW.project_id OR original.source_resource_id<>NEW.source_resource_id OR original.source_binding_id<>NEW.source_binding_id OR original.runner_id<>NEW.runner_id OR NEW.state<>'waiting_target' OR child.id IS NULL OR child.resource_id IS DISTINCT FROM NEW.target_resource_id OR child.binding_id IS DISTINCT FROM NEW.target_binding_id OR (NEW.plan->'operation'->>'version'<>'postgresql.compose-logical-local.v1' AND child.created_by<>NEW.created_by) OR child.project_id<>NEW.project_id OR NEW.plan->'source' IS DISTINCT FROM original.plan->'source' OR NEW.plan->'source_plan' IS DISTINCT FROM original.plan->'source_plan' OR NEW.plan->'source_storage' IS DISTINCT FROM original.plan->'source_storage' OR NEW.plan->'credentials' IS DISTINCT FROM original.plan->'credentials' THEN RAISE EXCEPTION 'database restore provenance or child mismatch' USING ERRCODE='23514'; END IF;
 END IF;
 IF NEW.plan->'operation'->>'version'='postgresql.compose-logical-local.v1' THEN
  IF EXISTS(SELECT 1 FROM hosting_operations o WHERE o.binding_id=CASE WHEN NEW.mode='backup' THEN NEW.source_binding_id ELSE NEW.target_binding_id END AND o.state NOT IN ('succeeded','failed','canceled')) OR NEW.plan->'component'->>'source' IS DISTINCT FROM NEW.plan->'operation'->>'source_component'
   OR NEW.plan->'operation'->>'source_deployed_operation_id' IS DISTINCT FROM source.id
   OR NOT(NEW.plan->'source' ? 'stack') OR NOT(NEW.plan->'source_plan' ? 'stack')
   OR NEW.plan->'source'->>'container_id' IS DISTINCT FROM ''
   OR NOT EXISTS(SELECT 1 FROM runners r WHERE r.id=NEW.runner_id AND r.status='active' AND r.token_hash<>'' AND r.capabilities @> ARRAY['hosting_compose_postgresql_backup_v1','hosting_managed_compose_v1','hosting_postgresql_backup_v1']::text[] AND NOT('shell'=ANY(r.capabilities)))
  THEN RAISE EXCEPTION 'component backup requires exact stack and configured agent' USING ERRCODE='23514'; END IF;
  IF NEW.mode='restore' AND (
   NEW.plan->'component'->>'target' IS DISTINCT FROM NEW.plan->'operation'->>'target_component'
   OR NEW.plan->'operation'->>'target_deployed_operation_id' IS DISTINCT FROM child.id
   OR child.state IS DISTINCT FROM 'succeeded'
   OR NEW.plan->'target'->>'operation_id' IS DISTINCT FROM child.id
   OR NEW.plan->'target'->'snapshot'->>'destination_id' IS DISTINCT FROM NEW.plan->'source'->'snapshot'->>'destination_id'
   OR NEW.plan->'target'->'snapshot'->>'runner_id' IS DISTINCT FROM NEW.runner_id
   OR NOT EXISTS(SELECT 1 FROM hosting_apply_plans p JOIN hosting_binding_state b ON b.binding_id=NEW.target_binding_id WHERE p.operation_id=child.id AND p.body=NEW.plan->'target_plan' AND b.healthy=NEW.plan->'target' AND b.hold_operation_id IS NULL AND b.restore_admitted)
  ) THEN RAISE EXCEPTION 'component restore target is not exact settled stack' USING ERRCODE='23514'; END IF;
 ELSIF NEW.plan ? 'component' OR coalesce(NEW.plan->'operation'->>'version','') NOT IN('postgresql.logical-local.v1','mysql.logical-local.v1') THEN
  RAISE EXCEPTION 'unsupported database backup plan version' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;

CREATE OR REPLACE FUNCTION nerocd_database_backup_lease() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE b hosting_database_backups; r runners; spec jsonb;
BEGIN
 SELECT run_spec INTO spec FROM task_runs WHERE id=NEW.run_id;
 IF spec->>'type' IS DISTINCT FROM 'database_backup' THEN RETURN NEW; END IF;
 SELECT * INTO b FROM hosting_database_backups WHERE run_id=NEW.run_id;
 SELECT * INTO r FROM runners WHERE id=NEW.runner_id;
 IF b.id IS NULL OR b.runner_id<>r.id OR NOT b.ownership_held OR b.state<>'dispatched' OR r.status<>'active' OR r.token_hash='' OR NOT('database_backup'=ANY(r.capabilities)) OR NOT((CASE WHEN b.plan->'operation'->>'version'='mysql.logical-local.v1' THEN 'hosting_mysql_backup_v1' ELSE 'hosting_postgresql_backup_v1' END)=ANY(r.capabilities)) OR (b.plan->'operation'->>'version'='mysql.logical-local.v1' AND NOT('hosting_mysql_v1'=ANY(r.capabilities))) OR 'shell'=ANY(r.capabilities) OR spec<>jsonb_build_object('type','database_backup','inputs','{}'::jsonb,'hosting_operation_id',b.id,'hosting_mode',b.mode) OR EXISTS(SELECT 1 FROM run_leases WHERE run_id=NEW.run_id) THEN RAISE EXCEPTION 'database backup lease identity mismatch' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;

CREATE OR REPLACE FUNCTION nerocd_hosting_storage_lease() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE snap jsonb; mode text;
BEGIN
 SELECT sn.body,t.mode INTO snap,mode FROM hosting_task_targets t JOIN hosting_snapshots sn ON sn.operation_id=t.operation_id WHERE t.run_id=NEW.run_id;
 IF snap ? 'database' THEN
  PERFORM 1 FROM hosting_task_targets t JOIN task_runs tr ON tr.id=t.run_id JOIN hosting_operations op ON op.id=t.operation_id
  WHERE t.run_id=NEW.run_id AND t.runner_id=NEW.runner_id AND t.binding_id=snap->>'binding_id' AND NEW.runner_id=snap->>'runner_id' AND tr.project_id=op.project_id AND t.mode IN ('deploy','observe') AND tr.run_spec->>'type'='database_deploy' AND tr.run_spec->>'hosting_operation_id'=t.operation_id AND tr.run_spec->>'hosting_mode'=t.mode AND tr.run_spec - 'type' - 'hosting_operation_id' - 'hosting_mode' - 'inputs'='{}'::jsonb AND coalesce(tr.run_spec->'inputs','{}'::jsonb)='{}'::jsonb AND tr.workflow='{}'::jsonb;
  IF NOT FOUND THEN RAISE EXCEPTION 'database lease requires exact typed target' USING ERRCODE='23514'; END IF;
  PERFORM nerocd_hosting_database_execution_validate((SELECT operation_id FROM hosting_task_targets WHERE run_id=NEW.run_id));
  PERFORM id FROM runners WHERE id=NEW.runner_id AND status='active' AND token_hash<>'' AND 'hosting_owned_storage_v1'=ANY(capabilities) AND 'hosting_database_v1'=ANY(capabilities) AND 'database_deploy'=ANY(capabilities) AND (snap->'database'->>'engine'<>'mysql' OR 'hosting_mysql_v1'=ANY(capabilities)) AND NOT('shell'=ANY(capabilities)) FOR SHARE;
  IF NOT FOUND THEN RAISE EXCEPTION 'database requires explicit private database and storage capabilities' USING ERRCODE='23514'; END IF;
 ELSIF mode='deploy' AND snap ? 'storage' THEN
  PERFORM id FROM runners WHERE id=NEW.runner_id AND status='active' AND token_hash<>'' AND 'hosting_owned_storage_v1'=ANY(capabilities) AND 'image_release_ports_v1'=ANY(capabilities) FOR SHARE;
  IF NOT FOUND THEN RAISE EXCEPTION 'storage deployment requires explicit storage and release port capabilities' USING ERRCODE='23514'; END IF;
 END IF;
 RETURN NEW;
END $$;

CREATE OR REPLACE FUNCTION nerocd_database_connection_lease_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE e hosting_database_connection_executions; runner runners; target hosting_task_targets; c hosting_database_connections; binding_key text;
BEGIN
 SELECT * INTO target FROM hosting_task_targets WHERE run_id=NEW.run_id;
 IF NOT FOUND THEN RETURN NEW; END IF;
 SELECT * INTO e FROM hosting_database_connection_executions WHERE operation_id=target.operation_id;
 IF NOT FOUND THEN RETURN NEW; END IF;
 SELECT * INTO STRICT c FROM hosting_database_connections WHERE id=e.connection_id;
 SELECT b.database_binding_id INTO STRICT binding_key FROM hosting_database_bindings b WHERE b.id=c.binding_id;
 SELECT * INTO STRICT runner FROM runners WHERE id=NEW.runner_id FOR SHARE;
 IF runner.status<>'active' OR runner.token_hash='' OR 'shell'=ANY(runner.capabilities) OR target.runner_id<>runner.id OR NOT EXISTS(SELECT 1 FROM runner_enrollments n WHERE n.runner_id=runner.id AND n.used_at IS NOT NULL AND n.revoked_at IS NULL AND n.credential_hash=runner.token_hash) OR NOT EXISTS(SELECT 1 FROM hosting_database_network_holds h WHERE h.database_binding_id=binding_key AND h.connection_id=c.id AND h.operation_id=e.operation_id) THEN RAISE EXCEPTION 'connection lease target or hold mismatch' USING ERRCODE='23514'; END IF;
 IF EXISTS(SELECT 1 FROM hosting_databases WHERE binding_id=binding_key AND engine='mysql') AND NOT('hosting_mysql_v1'=ANY(runner.capabilities)) THEN RAISE EXCEPTION 'MySQL connection capability required' USING ERRCODE='23514'; END IF;
 IF e.family='application' THEN
  IF NOT ('hosting_application_database_v1'=ANY(runner.capabilities) AND 'hosting_runtime_env_v1'=ANY(runner.capabilities) AND 'image_release_ports_v1'=ANY(runner.capabilities)) THEN RAISE EXCEPTION 'connected application capability required' USING ERRCODE='23514'; END IF;
 ELSE
  IF NOT ('hosting_database_membership_maintenance_v1'=ANY(runner.capabilities) AND 'hosting_database_v1'=ANY(runner.capabilities) AND 'hosting_owned_storage_v1'=ANY(runner.capabilities) AND 'database_deploy'=ANY(runner.capabilities)) THEN RAISE EXCEPTION 'member maintenance capability required' USING ERRCODE='23514'; END IF;
 END IF;
 PERFORM nerocd_database_connection_validate(e.operation_id,false);
 RETURN NEW;
END $$;


-- Engine and archive formats are never inferred across native backup families.
CREATE FUNCTION nerocd_mysql_backup_consistent() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE engine_key text; version_key text; format_key text;
BEGIN
 version_key:=NEW.plan->'operation'->>'version';
 IF version_key='postgresql.compose-logical-local.v1' THEN RETURN NEW; END IF;
 SELECT engine INTO STRICT engine_key FROM hosting_databases WHERE resource_id=NEW.source_resource_id;
 IF engine_key='mysql' THEN version_key:='mysql.logical-local.v1';format_key:='mysql_sql';
 ELSIF engine_key='postgresql' THEN version_key:='postgresql.logical-local.v1';format_key:='postgresql_custom';
 ELSE RAISE EXCEPTION 'unsupported native backup engine' USING ERRCODE='23514'; END IF;
 IF NEW.plan->'operation'->>'version' IS DISTINCT FROM version_key OR NEW.plan->'source'->'snapshot'->'database'->>'engine' IS DISTINCT FROM engine_key OR (NEW.blob IS NOT NULL AND NEW.blob->>'format' IS DISTINCT FROM format_key) OR (NEW.plan->'operation' ? 'blob' AND NEW.plan->'operation'->'blob'->>'format' IS DISTINCT FROM format_key) THEN RAISE EXCEPTION 'native backup engine or archive format mismatch' USING ERRCODE='23514'; END IF;
 IF NEW.mode='restore' AND NOT EXISTS(SELECT 1 FROM hosting_databases target JOIN hosting_databases original ON original.resource_id=NEW.source_resource_id WHERE target.resource_id=NEW.target_resource_id AND target.engine=original.engine AND target.recipe_version=original.recipe_version) THEN RAISE EXCEPTION 'restore target engine mismatch' USING ERRCODE='23514'; END IF;
 IF engine_key='mysql' AND (NEW.plan->'operation' ? 'remote_target' OR EXISTS(SELECT 1 FROM hosting_database_backup_remote_selections WHERE operation_id=NEW.id)) THEN RAISE EXCEPTION 'MySQL remote backup is unsupported' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER hosting_mysql_backup_consistency BEFORE INSERT OR UPDATE ON hosting_database_backups FOR EACH ROW EXECUTE FUNCTION nerocd_mysql_backup_consistent();
CREATE OR REPLACE FUNCTION nerocd_database_network_capture_lease() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE c hosting_database_network_captures;
BEGIN
 SELECT * INTO c FROM hosting_database_network_captures WHERE run_id=NEW.run_id;
 IF NOT FOUND THEN RETURN NEW; END IF;
 IF NEW.runner_id IS DISTINCT FROM c.plan->>'runner_id' OR EXISTS(SELECT 1 FROM hosting_database_network_origin_receipts WHERE capture_id=c.id) OR NOT EXISTS(SELECT 1 FROM runners WHERE id=NEW.runner_id AND status='active' AND token_hash<>'' AND 'hosting_database_network_capture_v1'=ANY(capabilities) AND (c.plan->'source_healthy'->'snapshot'->'database'->>'engine'<>'mysql' OR 'hosting_mysql_v1'=ANY(capabilities)) AND NOT('shell'=ANY(capabilities))) OR NOT EXISTS(SELECT 1 FROM runner_enrollments e JOIN runners r ON r.id=e.runner_id WHERE e.runner_id=NEW.runner_id AND e.used_at IS NOT NULL AND e.revoked_at IS NULL AND e.credential_hash=r.token_hash) THEN RAISE EXCEPTION 'database network capture runner denied' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;

DO $migration$
DECLARE owner_schema text:=current_schema(); routine text;
BEGIN
 FOREACH routine IN ARRAY ARRAY['nerocd_hosting_database_validate(text)','nerocd_hosting_database_execution_validate(text)','nerocd_database_binding_consistent()','nerocd_database_backup_admission()','nerocd_database_backup_lease()','nerocd_hosting_storage_lease()','nerocd_database_connection_lease_guard()','nerocd_mysql_backup_consistent()','nerocd_database_network_capture_lease()'] LOOP
  EXECUTE format('ALTER FUNCTION %I.%s SET search_path TO pg_catalog, %I, pg_temp',owner_schema,routine,owner_schema);
 END LOOP;
END;$migration$;
