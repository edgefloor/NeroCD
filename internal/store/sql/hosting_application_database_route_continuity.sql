-- name: ApplicationDatabaseRouteSchemaPresent :one
SELECT (to_regclass(current_schema()||'.hosting_application_database_route_continuations') IS NOT NULL)::boolean;
-- name: ApplicationDatabaseRouteSchemaCompatible :one
SELECT nerocd_appdb_route_schema_compatible()::boolean;
-- name: ReadApplicationDatabaseRouteContinuation :one
SELECT body FROM hosting_application_database_route_continuations WHERE operation_id=$1;
-- name: ReadApplicationDatabaseYield :one
SELECT request FROM hosting_application_database_yields WHERE operation_id=$1;
-- name: ApplicationDatabaseRouteRunnerEligible :one
SELECT (status='active' AND token_hash<>'' AND NOT('shell'=ANY(capabilities)) AND 'hosting_application_database_route_continuity_v1'=ANY(capabilities) AND 'hosting_proxy_verified_switch_v1'=ANY(capabilities) AND 'proxy_apply'=ANY(capabilities))::boolean FROM runners WHERE id=$1 FOR SHARE;
-- name: ConnectionRouteDeploymentSupported :one
SELECT (kind='application' AND source->>'kind'='image' AND NOT EXISTS(SELECT 1 FROM hosting_storage WHERE resource_id=sqlc.arg(application_resource_id)) AND NOT EXISTS(SELECT 1 FROM hosting_database_bindings WHERE database_resource_id=sqlc.arg(database_resource_id) AND application_resource_id<>sqlc.arg(application_resource_id)))::boolean FROM resources WHERE id=sqlc.arg(application_resource_id);
-- name: InsertApplicationDatabaseRouteContinuation :exec
INSERT INTO hosting_application_database_route_continuations(operation_id,proxy_id,proxy_operation_id,finalize_run_id,body,body_hash) VALUES($1,$2,$3,$4,$5,$6);
-- name: DeferApplicationDatabaseYield :exec
SELECT nerocd_appdb_yield_defer();
-- name: InsertApplicationDatabaseYield :exec
INSERT INTO hosting_application_database_yields(operation_id,receipt_id,request,request_hash) VALUES($1,$2,$3,$4);
-- name: ApplicationDatabaseFinalizeTargetExists :one
SELECT EXISTS(SELECT 1 FROM hosting_task_targets WHERE run_id=$1)::boolean;
-- name: CreateApplicationDatabaseFinalizeTarget :exec
INSERT INTO hosting_task_targets(run_id,operation_id,binding_id,runner_id,mode) VALUES($1,$2,$3,$4,'finalize');
-- name: ApplicationDatabaseRouteOrigin :one
SELECT operation_id FROM hosting_application_database_route_continuations WHERE proxy_operation_id=$1;
-- name: RecoverApplicationDatabaseRoutes :many
SELECT c.operation_id FROM hosting_application_database_route_continuations c JOIN hosting_application_database_yields y ON y.operation_id=c.operation_id JOIN hosting_operations o ON o.id=c.operation_id WHERE o.state IN ('applying','verifying') AND NOT EXISTS(SELECT 1 FROM hosting_task_targets t WHERE t.run_id=c.finalize_run_id) ORDER BY o.updated_at,o.id LIMIT 100;

-- name: ApplicationDatabaseFinalizeClaimFresh :one
SELECT (next_attempt=1 AND NOT EXISTS(SELECT 1 FROM run_leases WHERE run_id=t.id))::boolean FROM task_runs t WHERE t.id=$1;
