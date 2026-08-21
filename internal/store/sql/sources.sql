-- name: ListRepositories :many
SELECT * FROM repositories
WHERE (sqlc.arg(project_id)::text='' OR project_id=sqlc.arg(project_id))
ORDER BY name;

-- name: CreateRepository :one
INSERT INTO repositories (id,project_id,name,url,provider,default_ref,repository_policy,created_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING *;

-- name: LockRepositoryForPolicyConfiguration :one
SELECT * FROM repositories WHERE id=$1 AND project_id=$2 FOR UPDATE;

-- name: RepositoryPolicyActorAuthorized :one
SELECT EXISTS (
  SELECT 1 FROM users WHERE users.id=$2 AND users.global_role='system_admin'
  UNION ALL
  SELECT 1 FROM project_members WHERE project_id=$1 AND user_id=$2 AND role IN ('owner','maintainer')
);

-- name: GetRepositoryPolicyConfigurationReceipt :one
SELECT repository_id, configuration_id, actor_id, policy_sha256, audit_id, created_at
FROM repository_policy_configuration_receipts
WHERE repository_id=$1 AND configuration_id=$2;

-- name: SetRepositoryPolicyConfiguration :exec
UPDATE repositories SET repository_policy=$2, updated_at=clock_timestamp()
WHERE id=$1 AND repository_policy->>'state'='legacy_unverified';

-- name: CreateRepositoryPolicyConfigurationReceipt :exec
INSERT INTO repository_policy_configuration_receipts (repository_id, configuration_id, actor_id, policy_sha256, audit_id, created_at)
VALUES ($1,$2,$3,$4,$5,clock_timestamp());

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
