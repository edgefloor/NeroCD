-- name: DatabaseConnectionSchemaPresent :one
SELECT (to_regclass(current_schema()||'.hosting_database_connections') IS NOT NULL)::boolean;
-- name: DatabaseConnectionSchema :one
SELECT nerocd_database_connection_schema_compatible()::boolean;
-- name: DeferDatabaseConnectionAdmission :exec
SELECT nerocd_database_connection_defer_admission();
-- name: ReadDatabaseConnection :one
SELECT * FROM hosting_database_connections WHERE application_resource_id=$1;
-- name: ReadDatabaseConnectionByID :one
SELECT * FROM hosting_database_connections WHERE id=$1;
-- name: ReadDatabaseConnectionByDatabase :one
SELECT * FROM hosting_database_connections WHERE database_resource_id=$1;
-- name: LockDatabaseConnection :one
SELECT * FROM hosting_database_connections WHERE id=$1 FOR UPDATE;
-- name: InsertDatabaseConnection :exec
INSERT INTO hosting_database_connections(id,application_resource_id,database_resource_id,project_id,binding_id,source_operation_id,capture_id,upgrade_operation_id,application_operation_id,accepted_request,request_hash,binding_application_snapshot,state,reason_code,created_by,audit_id)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,'binding_saved','accepted',$13,$14);
-- name: AdvanceDatabaseConnection :execrows
UPDATE hosting_database_connections SET state=$2,reason_code=$3,updated_at=clock_timestamp() WHERE id=$1 AND state=$4;
-- name: PendingDatabaseConnections :many
SELECT id FROM hosting_database_connections WHERE state NOT IN('active','held') ORDER BY updated_at,id LIMIT 32 FOR UPDATE SKIP LOCKED;
-- name: TouchDatabaseConnection :exec
UPDATE hosting_database_connections SET updated_at=clock_timestamp() WHERE id=$1;
-- name: ConnectionApplicationSupported :one
SELECT (r.kind='application' AND r.source->>'kind'='image'
 AND NOT EXISTS(SELECT 1 FROM hosting_storage WHERE resource_id=r.id)
 AND NOT EXISTS(SELECT 1 FROM hosting_proxy_routes WHERE resource_id=r.id)
 AND NOT EXISTS(SELECT 1 FROM hosting_database_bindings WHERE database_resource_id=$2 AND application_resource_id<>r.id))::boolean
FROM resources r WHERE r.id=$1;
-- name: ConnectionOtherDatabaseBindings :one
SELECT EXISTS(SELECT 1 FROM hosting_database_bindings WHERE database_resource_id=$1 AND application_resource_id<>$2)::boolean;
-- name: EligibleConnectionDatabaseCandidates :many
SELECT db.id FROM resources app JOIN resource_bindings ab ON ab.resource_id=app.id
JOIN resources db ON db.project_id=app.project_id AND db.kind='database'
JOIN resource_bindings binding ON binding.resource_id=db.id AND binding.destination_id=ab.destination_id
JOIN hosting_binding_state state ON state.binding_id=binding.id
JOIN hosting_apply_plans plan ON plan.operation_id=state.healthy->>'operation_id'
WHERE app.id=$1 AND app.kind='application' AND app.source->>'kind'='image'
 AND plan.body->>'compiler_version'='database-hosting.private.owned-storage.v1'
 AND NOT EXISTS(SELECT 1 FROM hosting_database_bindings b WHERE b.database_resource_id=db.id)
 AND NOT EXISTS(SELECT 1 FROM hosting_database_network_holds h WHERE h.database_binding_id=binding.id)
ORDER BY db.id LIMIT 32;
-- name: ReadDatabaseMembershipHead :one
SELECT * FROM hosting_database_membership_heads WHERE database_binding_id=$1;
-- name: LockDatabaseMembershipHead :one
SELECT * FROM hosting_database_membership_heads WHERE database_binding_id=$1 FOR UPDATE;
-- name: InsertDatabaseMembershipHead :exec
INSERT INTO hosting_database_membership_heads(database_binding_id,connection_id,origin_receipt_id,revision,members,retired) VALUES($1,$2,$3,$4,'[]','[]');
-- name: AdvanceDatabaseMembershipHead :execrows
UPDATE hosting_database_membership_heads SET revision=$2,members=$3,retired=$4,updated_at=clock_timestamp() WHERE database_binding_id=$1 AND revision=$5;
-- name: ReadDatabaseConnectionExecution :one
SELECT * FROM hosting_database_connection_executions WHERE operation_id=$1;
-- name: InsertDatabaseConnectionExecution :exec
INSERT INTO hosting_database_connection_executions(operation_id,connection_id,family,expected_membership_revision,proof,proof_hash,audit_id) VALUES($1,$2,$3,$4,$5,$6,$7);
-- name: LockDatabaseNetworkHold :one
SELECT * FROM hosting_database_network_holds WHERE database_binding_id=$1 FOR UPDATE;
-- name: InsertDatabaseNetworkHold :exec
INSERT INTO hosting_database_network_holds(database_binding_id,connection_id,operation_id) VALUES($1,$2,$3);
-- name: AssignDatabaseNetworkHold :execrows
UPDATE hosting_database_network_holds SET operation_id=sqlc.narg(next_operation)::text WHERE database_binding_id=sqlc.arg(database_binding_id)::text AND connection_id=sqlc.arg(connection_id)::text AND operation_id IS NOT DISTINCT FROM sqlc.narg(previous_operation)::text;
-- name: ReleaseDatabaseNetworkHold :execrows
DELETE FROM hosting_database_network_holds WHERE database_binding_id=$1 AND connection_id=$2 AND operation_id=$3;
-- name: ReadApplicationDatabaseStage :one
SELECT * FROM hosting_application_database_stages WHERE operation_id=$1 ORDER BY CASE phase WHEN 'uncertain' THEN 6 WHEN 'ready' THEN 5 WHEN 'attached' THEN 4 WHEN 'network_configured' THEN 3 WHEN 'attach_intent' THEN 2 ELSE 1 END DESC LIMIT 1;
-- name: ReadApplicationDatabaseStageReceipt :one
SELECT * FROM hosting_application_database_stages WHERE receipt_id=$1;
-- name: InsertApplicationDatabaseStage :exec
INSERT INTO hosting_application_database_stages(operation_id,receipt_id,phase,previous_hash,authority,stage,stage_hash) VALUES($1,$2,$3,$4,$5,$6,$7);
-- name: ReadApplicationDatabaseRetirement :one
SELECT * FROM hosting_application_database_retirements WHERE operation_id=$1 ORDER BY CASE phase WHEN 'detached' THEN 1 ELSE 0 END DESC LIMIT 1;
-- name: ReadApplicationDatabaseRetirementPhase :one
SELECT * FROM hosting_application_database_retirements WHERE operation_id=$1 AND phase=$2;
-- name: InsertApplicationDatabaseRetirement :exec
INSERT INTO hosting_application_database_retirements(operation_id,phase,plan,receipt,receipt_id,receipt_hash,authority) VALUES($1,$2,$3,$4,$5,$6,$7);
-- name: InsertDatabaseMembershipReceipt :exec
INSERT INTO hosting_database_membership_receipts(database_binding_id,revision,operation_id,action,body,body_hash) VALUES($1,$2,$3,$4,$5,$6);
-- name: ReadApplicationDatabaseEffectAuthorization :one
SELECT * FROM hosting_application_database_effect_authorizations WHERE authorization_id=$1;
-- name: InsertApplicationDatabaseEffectAuthorization :exec
INSERT INTO hosting_application_database_effect_authorizations(authorization_id,operation_id,request,request_hash,response,use_until) VALUES($1,$2,$3,$4,$5,$6);
-- name: ApplicationDatabaseEffectClock :one
SELECT clock_timestamp()::timestamptz AS database_now,expires_at FROM run_leases WHERE id=$1;
-- name: ReadDatabaseConnectionRecovery :one
SELECT * FROM hosting_database_connection_recoveries WHERE connection_id=$1 AND idempotency_key=$2;
-- name: InsertDatabaseConnectionRecovery :exec
INSERT INTO hosting_database_connection_recoveries(id,connection_id,operation_id,actor_id,idempotency_key,accepted_request,request_hash,audit_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8);
-- name: FlushDatabaseConnection :exec
SELECT nerocd_database_connection_flush();
-- name: ConnectionRuntimeSecretOwner :one
SELECT resource_id,role,version FROM hosting_database_credentials WHERE secret_id=$1;
-- name: ConnectionObservationRequested :one
SELECT EXISTS(SELECT 1 FROM hosting_database_connection_recoveries r JOIN hosting_database_connection_executions e ON e.operation_id=r.operation_id AND e.connection_id=r.connection_id JOIN audit_events a ON a.id=r.audit_id JOIN task_runs t ON t.id=sqlc.arg(run_id)::text WHERE r.operation_id=sqlc.arg(operation_id)::text AND a.actor_id=r.actor_id AND a.action='hosting.database_connection.observation_requested' AND a.target_id=r.operation_id AND a.metadata->>'observation_run_id'=t.id AND t.requested_by=r.actor_id)::boolean;
