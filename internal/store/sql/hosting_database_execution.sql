-- name: HostingDatabaseExecutionSchemaCompatible :one
SELECT nerocd_hosting_database_execution_schema_compatible()::boolean;

-- name: CreateHostingDatabaseDeployment :exec
INSERT INTO hosting_database_deployments(operation_id,resource_id,binding_id,destination_id,reference,credentials) VALUES($1,$2,$3,$4,$5,$6);

-- name: ReadHostingDatabaseDeployment :one
SELECT * FROM hosting_database_deployments WHERE operation_id=$1;
