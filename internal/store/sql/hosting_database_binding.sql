-- name: ReadHostingDatabaseBinding :one
SELECT * FROM hosting_database_bindings WHERE application_resource_id=$1;
-- name: ReadHostingDatabaseBindingReceipt :one
SELECT * FROM hosting_database_binding_receipts WHERE application_resource_id=$1 AND request_id=$2;
-- name: HostingDatabaseBindingPending :one
SELECT EXISTS(SELECT 1 FROM hosting_database_bindings WHERE application_resource_id=$1);
-- name: HostingDatabaseBindingSchemaPresent :one
SELECT (to_regclass(current_schema()||'.hosting_database_bindings') IS NOT NULL)::boolean AS present;
-- name: InsertHostingDatabaseBinding :exec
INSERT INTO hosting_database_bindings(id,application_resource_id,application_binding_id,database_resource_id,database_binding_id,project_id,destination_id,database_operation_id,runtime_generation,runtime_hash,credential_secret_id,credential_version,body,body_hash,created_by)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15);
-- name: InsertHostingDatabaseBindingReceipt :exec
INSERT INTO hosting_database_binding_receipts(application_resource_id,request_id,actor_id,accepted_request,request_hash,binding_hash,runtime_generation,runtime_hash,audit_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9);
-- name: HostingDatabaseBindingTargetSafe :one
SELECT NOT EXISTS(SELECT 1 FROM hosting_operations o WHERE o.binding_id=sqlc.arg(binding_id)::text AND o.state NOT IN('succeeded','failed','canceled'))
 AND NOT EXISTS(SELECT 1 FROM hosting_binding_state s WHERE s.binding_id=sqlc.arg(binding_id)::text AND (s.hold_operation_id IS NOT NULL OR NOT s.restore_admitted))
 AND NOT EXISTS(SELECT 1 FROM hosting_binding_restore_holds h WHERE h.binding_id=sqlc.arg(binding_id)::text) AS safe;
-- name: HostingDatabaseBindingApplicationSupported :one
SELECT r.kind='application' AND r.source->>'kind'='image' AND NOT EXISTS(SELECT 1 FROM hosting_storage WHERE resource_id=r.id) AS supported FROM resources r WHERE r.id=$1;
-- name: FlushHostingDatabaseBindingConstraints :exec
SELECT nerocd_database_binding_flush_constraints();

-- name: HostingDatabaseBindingSchemaCompatible :one
SELECT nerocd_database_binding_schema_compatible()::boolean;
