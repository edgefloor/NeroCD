-- name: ListRepositories :many
SELECT * FROM repositories
WHERE (sqlc.arg(project_id)::text='' OR project_id=sqlc.arg(project_id))
ORDER BY name;

-- name: CreateRepository :one
INSERT INTO repositories (id,project_id,name,url,provider,default_ref,created_at)
VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING *;

-- name: ListAccessKeys :many
SELECT * FROM access_keys
WHERE (sqlc.arg(project_id)::text='' OR project_id=sqlc.arg(project_id))
ORDER BY name;

-- name: CreateAccessKey :one
INSERT INTO access_keys (id,project_id,name,kind,fingerprint,created_at)
VALUES ($1,$2,$3,$4,$5,$6) RETURNING *;

-- name: ListInventories :many
SELECT * FROM inventories
WHERE (sqlc.arg(project_id)::text='' OR project_id=sqlc.arg(project_id))
ORDER BY name;

-- name: CreateInventory :one
INSERT INTO inventories (id,project_id,name,kind,source,created_at)
VALUES ($1,$2,$3,$4,$5,$6) RETURNING *;
