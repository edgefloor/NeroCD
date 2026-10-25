-- name: ApplicationDatabaseStoragePresent :one
SELECT (to_regclass(current_schema()||'.hosting_application_database_storage') IS NOT NULL)::boolean;
-- name: ApplicationDatabaseStorageCompatible :one
SELECT nerocd_appdb_storage_schema_compatible()::boolean;
-- name: ReadApplicationDatabaseStorage :one
SELECT body FROM hosting_application_database_storage WHERE operation_id=$1;
-- name: InsertApplicationDatabaseStorage :exec
INSERT INTO hosting_application_database_storage(operation_id,storage_id,body,body_hash) VALUES($1,$2,$3,$4);
-- name: ApplicationDatabaseStorageRunnerEligible :one
SELECT EXISTS(SELECT 1 FROM runners r WHERE r.id=$1 AND r.status='active' AND r.token_hash<>'' AND NOT('shell'=ANY(r.capabilities)) AND 'hosting_application_database_storage_v1'=ANY(r.capabilities) AND 'hosting_application_database_v1'=ANY(r.capabilities) AND 'hosting_owned_storage_v1'=ANY(r.capabilities) AND 'hosting_runtime_env_v1'=ANY(r.capabilities) AND EXISTS(SELECT 1 FROM runner_enrollments e WHERE e.runner_id=r.id AND e.used_at IS NOT NULL AND e.revoked_at IS NULL AND e.credential_hash=r.token_hash));
-- name: ConnectionStorageDeploymentSupported :one
SELECT (r.kind='application' AND r.source->>'kind'='image' AND NOT EXISTS(SELECT 1 FROM hosting_database_bindings WHERE database_resource_id=sqlc.arg(database_resource_id) AND application_resource_id<>r.id) AND (sqlc.arg(routed)::boolean OR NOT EXISTS(SELECT 1 FROM hosting_proxy_routes WHERE resource_id=r.id AND enabled)))::boolean FROM resources r WHERE r.id=sqlc.arg(application_resource_id);
-- name: ReadApplicationDatabaseStorageWriter :one
SELECT request FROM hosting_application_database_storage_writers WHERE operation_id=$1 ORDER BY CASE phase WHEN 'resumed' THEN 6 WHEN 'resume_intent' THEN 5 WHEN 'replacement_failed' THEN 4 WHEN 'candidate_stopped' THEN 3 WHEN 'stopped' THEN 2 ELSE 1 END DESC LIMIT 1;
-- name: ReadApplicationDatabaseStorageWriterPhase :one
SELECT request FROM hosting_application_database_storage_writers WHERE operation_id=$1 AND phase=$2;
-- name: InsertApplicationDatabaseStorageWriter :exec
INSERT INTO hosting_application_database_storage_writers(operation_id,phase,receipt_id,request,request_hash,response) VALUES($1,$2,$3,$4,$5,$6);
-- name: ReadApplicationDatabaseStorageRecovery :one
SELECT action,run_id,proxy_operation_id,actor_id,request,request_hash,body FROM hosting_application_database_storage_recoveries WHERE operation_id=$1;
-- name: InsertApplicationDatabaseStorageRecovery :exec
INSERT INTO hosting_application_database_storage_recoveries(operation_id,action,run_id,proxy_operation_id,actor_id,request,request_hash,body) VALUES($1,$2,$3,$4,$5,$6,$7,$8);
-- name: ReadDatabaseNetworkHold :one
SELECT operation_id FROM hosting_database_network_holds WHERE database_binding_id=$1;

-- name: ReadApplicationDatabaseStorageWriterAck :one
SELECT request,response FROM hosting_application_database_storage_writers WHERE operation_id=$1 AND phase=$2;
-- name: ReadApplicationDatabaseStorageEffect :one
SELECT request,response,use_until FROM hosting_application_database_storage_effects WHERE authorization_id=$1;
-- name: InsertApplicationDatabaseStorageEffect :exec
INSERT INTO hosting_application_database_storage_effects(authorization_id,operation_id,request,response,use_until) VALUES($1,$2,$3,$4,$5);
-- name: ReadApplicationDatabaseStorageTaskStatus :one
SELECT status FROM task_runs WHERE id=$1;
-- name: ApplicationDatabaseStorageOriginalEnded :one
SELECT NOT EXISTS(SELECT 1 FROM run_leases WHERE run_id=$1 AND status='active' AND expires_at>clock_timestamp())::boolean;
-- name: ApplicationDatabaseStorageFinalizeUnused :one
SELECT NOT EXISTS(SELECT 1 FROM run_leases WHERE run_id='finalize_'||sqlc.arg(operation_id)::text)::boolean;
-- name: SetApplicationDatabaseStorageResumed :execrows
UPDATE hosting_operations SET state='failed',updated_at=clock_timestamp() WHERE id=$1 AND state='failed' AND NOT EXISTS(SELECT 1 FROM hosting_application_database_yields WHERE operation_id=$1);
-- name: ReleaseApplicationDatabaseStorageNetworkHold :execrows
DELETE FROM hosting_database_network_holds WHERE operation_id=$1;

-- name: InsertApplicationDatabaseStorageResumeMembership :exec
INSERT INTO hosting_application_database_storage_membership_receipts(database_binding_id,revision,operation_id,body,body_hash) VALUES($1,$2,$3,$4,$5);
-- name: CreateApplicationDatabaseStorageRecoveryTarget :exec
INSERT INTO hosting_task_targets(run_id,operation_id,binding_id,runner_id,mode) VALUES($1,$2,$3,$4,'storage_recover');

-- name: ApplicationDatabaseStorageProxyOrigin :one
SELECT operation_id FROM hosting_application_database_storage_recoveries WHERE proxy_operation_id=$1 AND action='complete_replacement';

-- name: ApplicationDatabaseStorageRecoveryLeaseLive :one
SELECT EXISTS(SELECT 1 FROM run_leases WHERE run_id=$1 AND status='active' AND expires_at>clock_timestamp())::boolean;
-- name: DeferApplicationDatabaseStorageWriterSettlement :exec
SELECT nerocd_appdb_storage_writer_defer();

-- name: ApplicationDatabaseStorageOwnerCurrent :one
SELECT EXISTS(SELECT 1 FROM projects p JOIN principals actor ON actor.id=sqlc.arg(actor_id)::text WHERE p.id=sqlc.arg(project_id)::text AND p.archived_at IS NULL AND (
 (actor.kind='human' AND EXISTS(SELECT 1 FROM users u WHERE u.id=actor.id AND u.status='active' AND u.archived_at IS NULL AND (u.global_role='system_admin' OR EXISTS(SELECT 1 FROM project_members m WHERE m.project_id=p.id AND m.user_id=u.id AND m.role='owner')))) OR
 (actor.kind='service_account' AND EXISTS(SELECT 1 FROM service_accounts sa JOIN users u ON u.id=sa.owner_user_id WHERE sa.id=actor.id AND sa.project_id=p.id AND sa.role='owner' AND sa.status='active' AND u.status='active' AND u.archived_at IS NULL)) OR
 (actor.kind='api_token' AND EXISTS(SELECT 1 FROM api_tokens token WHERE token.id=actor.id AND token.service_account_id IS NULL AND token.status='active' AND token.revoked_at IS NULL AND (token.expires_at IS NULL OR token.expires_at>clock_timestamp()) AND 'system_admin'=ANY(token.roles)))
));
