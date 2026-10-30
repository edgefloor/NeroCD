-- name: FindDatabaseImportStageRequest :one
SELECT * FROM hosting_database_import_stages WHERE target_resource_id=$1 AND request_id=$2 FOR UPDATE;

-- name: ReadDatabaseImportStage :one
SELECT * FROM hosting_database_import_stages WHERE id=$1;

-- name: LockDatabaseImportStage :one
SELECT * FROM hosting_database_import_stages WHERE id=$1 FOR UPDATE;

-- name: CreateDatabaseImportStage :exec
INSERT INTO hosting_database_import_stages(id,project_id,target_resource_id,target_binding_id,actor_id,request_id,request_hash,runner_id,daemon_id,plan,plan_hash,body) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12);

-- name: SetDatabaseImportStage :exec
UPDATE hosting_database_import_stages SET body=$2,ready_receipt=$3,updated_at=clock_timestamp() WHERE id=$1;

-- name: DispatchDatabaseImportStage :exec
UPDATE hosting_database_import_stages SET run_id=$2,updated_at=clock_timestamp() WHERE id=$1 AND run_id IS NULL;

-- name: FindDatabaseImportRequest :one
SELECT * FROM hosting_database_imports WHERE target_resource_id=$1 AND request_id=$2 FOR UPDATE;

-- name: ReadDatabaseImport :one
SELECT * FROM hosting_database_imports WHERE id=$1;

-- name: LockDatabaseImport :one
SELECT * FROM hosting_database_imports WHERE id=$1 FOR UPDATE;

-- name: CreateDatabaseImport :exec
INSERT INTO hosting_database_imports(id,stage_id,project_id,target_resource_id,target_binding_id,actor_id,request_id,request_hash,runner_id,daemon_id,plan,plan_hash,body,ownership_held) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,true);

-- name: SetDatabaseImport :exec
UPDATE hosting_database_imports SET body=$2,ownership_held=$3,updated_at=clock_timestamp() WHERE id=$1;

-- name: DispatchDatabaseImport :exec
UPDATE hosting_database_imports SET run_id=$2,updated_at=clock_timestamp() WHERE id=$1 AND run_id IS NULL;

-- name: ExternalImportTargetOwnershipHeld :one
SELECT EXISTS(SELECT 1 FROM hosting_database_imports WHERE target_binding_id=$1 AND ownership_held)::boolean;

-- name: ReadDatabaseImportReceipt :one
SELECT * FROM hosting_database_import_receipts WHERE receipt_id=$1;

-- name: CreateDatabaseImportReceipt :exec
INSERT INTO hosting_database_import_receipts(operation_id,phase,receipt_id,authority,body,body_hash,response) VALUES($1,$2,$3,$4,$5,$6,$7);

-- name: LockDatabaseImportStageAuthority :one
SELECT l.* FROM hosting_database_import_stages s JOIN run_leases l ON l.run_id=s.run_id JOIN task_runs t ON t.id=l.run_id WHERE s.id=$1 AND s.run_id=$2 AND s.runner_id=$3 AND l.runner_id=$3 AND l.id=$4 AND l.attempt=$5 AND l.fence=$6 AND (NOT $7::boolean OR (l.status='active' AND l.expires_at>clock_timestamp())) AND t.run_spec=jsonb_build_object('type','database_import_stage','hosting_operation_id',s.id,'hosting_mode','stage','inputs','{}'::jsonb) FOR UPDATE OF l;

-- name: LockDatabaseImportAuthority :one
SELECT l.* FROM hosting_database_imports s JOIN run_leases l ON l.run_id=s.run_id JOIN task_runs t ON t.id=l.run_id WHERE s.id=$1 AND s.run_id=$2 AND s.runner_id=$3 AND l.runner_id=$3 AND l.id=$4 AND l.attempt=$5 AND l.fence=$6 AND (NOT $7::boolean OR (l.status='active' AND l.expires_at>clock_timestamp())) AND t.run_spec=jsonb_build_object('type','database_import','hosting_operation_id',s.id,'hosting_mode','import','inputs','{}'::jsonb) FOR UPDATE OF l;

-- name: ReadDatabaseImportIntentBody :one
SELECT body FROM hosting_database_import_receipts WHERE operation_id=$1 AND phase='import_intent';

-- name: ReadDatabaseImportDelivery :one
SELECT metadata FROM hosting_database_import_deliveries WHERE delivery_id=$1;

-- name: ReadDatabaseImportStageDelivery :one
SELECT metadata FROM hosting_database_import_deliveries WHERE delivery_id=$1 AND stage_id=$2;

-- name: CreateDatabaseImportDelivery :exec
INSERT INTO hosting_database_import_deliveries(delivery_id,stage_id,metadata) VALUES($1,$2,$3);

-- name: DatabaseImportStageDeadlinePassed :one
SELECT ($1::timestamptz<=clock_timestamp())::boolean;
