-- name: CreateHostingServer :one
INSERT INTO servers(id,name,runner_id,created_by,created_at) VALUES($1,$2,$3,$4,$5) RETURNING *;
-- name: CreateHostingDestination :one
INSERT INTO destinations(id,server_id,name,runtime_kind,created_at) VALUES($1,$2,$3,$4,$5) RETURNING *;
-- name: GetHostingServer :one
SELECT * FROM servers WHERE id=$1;
-- name: GetHostingDestination :one
SELECT * FROM destinations WHERE id=$1;
-- name: LockHostingDestination :one
SELECT * FROM destinations WHERE id=$1 FOR UPDATE;
-- name: GetDestinationProjectGrant :one
SELECT * FROM destination_project_grants WHERE destination_id=$1 AND project_id=$2;
-- name: GrantDestinationProject :one
INSERT INTO destination_project_grants(destination_id,project_id,granted_by,created_at) VALUES($1,$2,$3,$4)
ON CONFLICT(destination_id,project_id) DO UPDATE SET granted_by=excluded.granted_by,created_at=excluded.created_at,revoked_at=NULL WHERE destination_project_grants.revoked_at IS NOT NULL RETURNING *;
-- name: RevokeDestinationProject :one
UPDATE destination_project_grants SET revoked_at=$3 WHERE destination_id=$1 AND project_id=$2 AND revoked_at IS NULL RETURNING *;
-- name: GetResourceBinding :one
SELECT * FROM resource_bindings WHERE resource_id=$1;
-- name: CreateResourceBinding :one
INSERT INTO resource_bindings(id,resource_id,destination_id,runtime_namespace,created_by,created_at) VALUES($1,$2,$3,$4,$5,$6) RETURNING *;
-- name: GetBindingResourceProject :one
SELECT project_id FROM resources WHERE id=$1;
-- name: CountHostingServers :one
SELECT count(*) FROM servers;
-- name: ListHostingServers :many
SELECT * FROM servers ORDER BY created_at,id LIMIT sqlc.arg(page_limit) OFFSET sqlc.arg(page_offset);
-- name: CountHostingDestinations :one
SELECT count(*) FROM destinations WHERE (sqlc.arg(server_id)::text='' OR server_id=sqlc.arg(server_id));
-- name: ListHostingDestinations :many
SELECT * FROM destinations WHERE (sqlc.arg(server_id)::text='' OR server_id=sqlc.arg(server_id)) ORDER BY created_at,id LIMIT sqlc.arg(page_limit) OFFSET sqlc.arg(page_offset);
-- name: CountDestinationProjectGrants :one
SELECT count(*) FROM destination_project_grants WHERE destination_id=sqlc.arg(destination_id);
-- name: ListDestinationProjectGrants :many
SELECT * FROM destination_project_grants WHERE destination_id=sqlc.arg(destination_id) ORDER BY created_at,project_id LIMIT sqlc.arg(page_limit) OFFSET sqlc.arg(page_offset);
-- name: CountResourceBindings :one
SELECT count(*) FROM resource_bindings WHERE destination_id=sqlc.arg(destination_id);
-- name: ListResourceBindings :many
SELECT * FROM resource_bindings WHERE destination_id=sqlc.arg(destination_id) ORDER BY created_at,id LIMIT sqlc.arg(page_limit) OFFSET sqlc.arg(page_offset);
-- name: CountProjectDestinations :one
SELECT count(*) FROM destinations d JOIN destination_project_grants g ON g.destination_id=d.id WHERE g.project_id=$1 AND g.revoked_at IS NULL;
-- name: ListProjectDestinations :many
SELECT d.* FROM destinations d JOIN destination_project_grants g ON g.destination_id=d.id WHERE g.project_id=$1 AND g.revoked_at IS NULL ORDER BY d.created_at,d.id LIMIT $2 OFFSET $3;
-- name: GetActiveDestinationProject :one
SELECT id FROM projects WHERE id=$1 AND archived_at IS NULL;
-- name: DestinationPrincipalExists :one
SELECT EXISTS(SELECT 1 FROM principals WHERE id=$1);
