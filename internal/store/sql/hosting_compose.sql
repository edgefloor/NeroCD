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

-- name: GetHostingComposeVolume :one
SELECT * FROM hosting_compose_volumes WHERE binding_id=$1 AND name=$2 FOR UPDATE;

-- name: AdmitHostingComposeVolume :exec
INSERT INTO hosting_compose_volumes(binding_id,name,volume_name,destination_id,daemon_id,creation_operation_id,creation_plan_hash,creation_nonce,state)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,'admitted');

-- name: MaterializeHostingComposeVolume :execrows
UPDATE hosting_compose_volumes SET state='materialized',created_at=$3 WHERE binding_id=$1 AND name=$2 AND state='admitted';

-- name: HostingStackMutationHistory :one
SELECT EXISTS(SELECT 1 FROM hosting_apply_admissions a JOIN hosting_operations o ON o.id=a.operation_id WHERE o.binding_id=$1)
OR EXISTS(SELECT 1 FROM hosting_compose_volumes WHERE binding_id=$1)
OR EXISTS(SELECT 1 FROM hosting_retained_releases WHERE binding_id=$1)
OR EXISTS(SELECT 1 FROM hosting_release_claims WHERE binding_id=$1 AND state<>'released')
OR EXISTS(SELECT 1 FROM hosting_proxy_routes WHERE binding_id=$1) AS recorded;
