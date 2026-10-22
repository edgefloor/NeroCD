-- name: LockHostingRuntimeResource :one
SELECT id,project_id,kind,source,generation FROM resources WHERE id=$1 FOR UPDATE;
-- name: ReadHostingRuntimeResource :one
SELECT id,project_id,kind,source,generation FROM resources WHERE id=$1 FOR SHARE;
-- name: LockHostingRuntimeConfiguration :one
SELECT generation FROM hosting_runtime_configurations WHERE resource_id=$1 FOR UPDATE;
-- name: ReadHostingRuntimeGeneration :one
SELECT generation FROM hosting_runtime_configurations WHERE resource_id=$1 FOR SHARE;
-- name: CreateHostingRuntimeConfiguration :exec
INSERT INTO hosting_runtime_configurations(resource_id,project_id,generation) VALUES($1,$2,1);
-- name: AdvanceHostingRuntimeConfiguration :execrows
UPDATE hosting_runtime_configurations SET generation=generation+1 WHERE resource_id=$1 AND generation=$2 AND generation<9223372036854775807;
-- name: InsertHostingRuntimeVersion :one
INSERT INTO hosting_runtime_configuration_versions(resource_id,project_id,generation,body,body_hash,created_by) VALUES($1,$2,$3,$4,$5,$6) RETURNING *;
-- name: GetHostingRuntimeVersion :one
SELECT * FROM hosting_runtime_configuration_versions WHERE resource_id=$1 AND generation=$2;
-- name: InsertHostingRuntimeSecretReference :exec
INSERT INTO hosting_runtime_configuration_secret_refs(resource_id,project_id,generation,secret_id,secret_version) VALUES($1,$2,$3,$4,$5);
-- name: ValidateHostingRuntimeSecretReference :one
SELECT s.id FROM project_managed_secrets s JOIN project_managed_secret_versions v ON v.secret_id=s.id WHERE s.id=$1 AND s.project_id=$2 AND v.version=$3 AND s.status='active' FOR SHARE OF s;
-- name: GetHostingRuntimeReceipt :one
SELECT * FROM hosting_runtime_configuration_receipts WHERE resource_id=$1 AND request_id=$2;
-- name: InsertHostingRuntimeReceipt :exec
INSERT INTO hosting_runtime_configuration_receipts(resource_id,project_id,request_id,actor_id,request_hash,expected_resource_generation,expected_runtime_generation,runtime_generation,audit_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9);
