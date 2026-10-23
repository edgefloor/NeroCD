-- name: HostingDatabaseSchemaCompatible :one
SELECT nerocd_hosting_database_schema_compatible()::boolean;

-- name: HostingDatabaseSecretOwned :one
SELECT EXISTS(SELECT 1 FROM hosting_database_credentials WHERE secret_id=$1);

-- name: HostingDatabaseRequest :one
SELECT * FROM hosting_database_receipts WHERE project_id=$1 AND request_id=$2;

-- name: HostingDatabaseRead :one
SELECT * FROM hosting_databases WHERE resource_id=$1;

-- name: HostingDatabaseCredentials :many
SELECT * FROM hosting_database_credentials WHERE resource_id=$1 ORDER BY role;

-- name: HostingDatabaseInsert :exec
INSERT INTO hosting_databases(resource_id,project_id,binding_id,destination_id,storage_id,engine,recipe_version,image_reference,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9);

-- name: HostingDatabaseCredentialInsert :exec
INSERT INTO hosting_database_credentials(resource_id,role,username,secret_id,version) VALUES($1,$2,$3,$4,1);

-- name: HostingDatabaseReceiptInsert :exec
INSERT INTO hosting_database_receipts(project_id,request_id,resource_id,actor_id,request_hash,accepted_request,audit_id) VALUES($1,$2,$3,$4,$5,$6,$7);
