-- name: DatabaseNetworkUpgradeSchemaPresent :one
SELECT (to_regclass(current_schema()||'.hosting_database_network_upgrades') IS NOT NULL)::boolean;
-- name: DatabaseNetworkUpgradeSchema :one
SELECT nerocd_database_network_upgrade_schema_compatible()::boolean;
-- name: ReadDatabaseNetworkUpgrade :one
SELECT * FROM hosting_database_network_upgrades WHERE operation_id=$1;
-- name: InsertDatabaseNetworkUpgrade :exec
INSERT INTO hosting_database_network_upgrades(operation_id,capture_id,origin_receipt_id,previous_operation_id,proof,proof_hash,audit_id) VALUES($1,$2,$3,$4,$5,$6,$7);
-- name: FlushDatabaseNetworkUpgrade :exec
SELECT nerocd_database_network_upgrade_flush();
