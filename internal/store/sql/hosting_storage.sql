-- name: HostingStorageSchemaCompatible :one
SELECT nerocd_hosting_storage_schema_compatible()::boolean;

-- name: LockHostingStorageGrant :one
SELECT destination_id FROM destination_project_grants WHERE destination_id=$1 AND project_id=$2 AND revoked_at IS NULL FOR SHARE;

-- name: ReadHostingStorage :one
SELECT * FROM hosting_storage WHERE resource_id=$1;

-- name: ReadHostingStorageReceipt :one
SELECT * FROM hosting_storage_receipts WHERE resource_id=$1 AND request_id=$2;

-- name: InsertHostingStorage :one
INSERT INTO hosting_storage(id,project_id,resource_id,binding_id,destination_id,kind,driver,volume_name,mount_path,access_mode,retention,generation,body_hash,created_by)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,1,$12,$13) RETURNING *;

-- name: InsertHostingStorageReceipt :exec
INSERT INTO hosting_storage_receipts(resource_id,request_id,storage_id,actor_id,request_hash,expected_resource_generation,audit_id) VALUES($1,$2,$3,$4,$5,$6,$7);
