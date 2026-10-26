-- name: AppendHostingComposeSourceVersion :exec
INSERT INTO hosting_compose_source_versions(resource_id,generation,original,original_hash,definition,definition_hash)
VALUES($1,$2,$3,$4,$5,$6);

-- name: GetHostingComposeSourceVersion :one
SELECT * FROM hosting_compose_source_versions WHERE resource_id=$1 AND generation=$2;

-- name: ClaimHostingComposeCreate :execrows
INSERT INTO hosting_compose_create_receipts(project_id,request_id,resource_id,request_hash)
VALUES($1,$2,$3,$4) ON CONFLICT(project_id,request_id) DO NOTHING;

-- name: GetHostingComposeCreate :one
SELECT * FROM hosting_compose_create_receipts WHERE project_id=$1 AND request_id=$2;
