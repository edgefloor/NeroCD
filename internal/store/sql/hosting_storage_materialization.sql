-- name: HostingStorageMaterializationSchemaCompatible :one
SELECT nerocd_hosting_storage_materialization_schema_compatible()::boolean;
-- name: LockHostingStorageMaterialization :one
SELECT m.origin,m.origin_hash,r.receipt_id,r.body_hash,r.created_at FROM hosting_storage_materializations m LEFT JOIN hosting_storage_materialized_receipts r ON r.operation_id=m.materialized_operation_id AND r.receipt_id=m.materialized_receipt_id WHERE m.storage_id=$1 FOR UPDATE OF m;
-- name: InsertHostingStorageMaterialization :exec
INSERT INTO hosting_storage_materializations(storage_id,origin_operation_id,origin_plan_hash,origin,origin_hash) VALUES($1,$2,$3,$4,$5);
-- name: ReadHostingStorageOperationAdmission :one
SELECT * FROM hosting_storage_operation_admissions WHERE operation_id=$1;
-- name: InsertHostingStorageOperationAdmission :exec
INSERT INTO hosting_storage_operation_admissions(operation_id,storage_id,plan_hash,authority) VALUES($1,$2,$3,$4);
-- name: ReadHostingStorageMaterializedReceipt :one
SELECT * FROM hosting_storage_materialized_receipts WHERE operation_id=$1;
-- name: InsertHostingStorageMaterializedReceipt :one
INSERT INTO hosting_storage_materialized_receipts(operation_id,storage_id,receipt_id,authority,body,body_hash,audit_id) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING *;
-- name: MarkHostingStorageMaterialized :execrows
UPDATE hosting_storage_materializations SET materialized_operation_id=$2,materialized_receipt_id=$3 WHERE storage_id=$1 AND materialized_operation_id IS NULL;
