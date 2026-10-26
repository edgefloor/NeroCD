-- name: ReadHostingCatalogInstallation :one
SELECT body,body_hash FROM hosting_catalog_installations
WHERE (project_id=sqlc.arg(project_id) AND request_id=sqlc.arg(request_id)) OR (resource_id=sqlc.arg(resource_id) AND sqlc.arg(resource_id)::text<>'');
-- name: ReadHostingCatalogInstallReceipt :one
SELECT request_hash,actor_id FROM hosting_catalog_installations WHERE project_id=$1 AND request_id=$2;
-- name: InsertHostingCatalogInstallation :exec
INSERT INTO hosting_catalog_installations(project_id,request_id,resource_id,actor_id,request_hash,body,body_hash) VALUES($1,$2,$3,$4,$5,$6,$7);
