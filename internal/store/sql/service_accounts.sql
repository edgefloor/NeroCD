-- name: CountServiceAccounts :one
SELECT count(*) FROM service_accounts WHERE project_id = $1;

-- name: ListServiceAccountsPage :many
SELECT * FROM service_accounts
WHERE project_id = $1
ORDER BY created_at DESC, id DESC
LIMIT $2 OFFSET $3;

-- name: LockActiveServiceAccount :one
SELECT sa.* FROM service_accounts sa
JOIN users owner_user ON owner_user.id = sa.owner_user_id
WHERE sa.id = $1 AND sa.status = 'active' AND owner_user.status = 'active'
FOR UPDATE OF sa, owner_user;

-- name: ServiceAccountProjectRole :one
SELECT sa.project_id, sa.role FROM service_accounts sa
JOIN users owner_user ON owner_user.id = sa.owner_user_id
WHERE sa.id = $1 AND sa.status = 'active' AND owner_user.status = 'active';

-- name: CreateServiceAccount :one
INSERT INTO service_accounts (id, project_id, owner_user_id, name, role, status, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, 'active', $6, $6)
RETURNING *;

-- name: UpdateServiceAccount :one
UPDATE service_accounts
SET name = COALESCE(NULLIF(sqlc.arg(name), ''), name),
    role = COALESCE(NULLIF(sqlc.arg(role), ''), role),
    updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id) AND status = 'active'
RETURNING *;

-- name: InsertManagedServiceAccountCredential :one
INSERT INTO api_tokens (id, name, kind, token_hash, roles, status, created_by, service_account_id, created_at, expires_at)
VALUES ($1, $2, 'service_account', $3, ARRAY[]::text[], 'active', $4, $5, $6, $7)
RETURNING *;

-- name: RevokeActiveServiceAccountCredentials :execrows
UPDATE api_tokens
SET status = 'revoked', revoked_at = $2
WHERE service_account_id = $1 AND status = 'active' AND revoked_at IS NULL;

-- name: RevokeServiceAccount :one
UPDATE service_accounts
SET status = 'revoked', revoked_at = $2, updated_at = $2
WHERE id = $1 AND status = 'active'
RETURNING *;

-- name: GetManagedServiceAccountCredentialByHash :one
WITH matched AS (
    SELECT token_row.id
    FROM api_tokens token_row
    JOIN service_accounts sa ON sa.id = token_row.service_account_id
    JOIN users owner_user ON owner_user.id = sa.owner_user_id
    WHERE token_row.token_hash = $1
      AND token_row.status = 'active' AND token_row.revoked_at IS NULL
      AND token_row.expires_at IS NOT NULL AND token_row.expires_at > $2
      AND sa.status = 'active' AND owner_user.status = 'active'
), touched AS (
    UPDATE api_tokens token_row SET last_used_at = $2
    FROM matched WHERE token_row.id = matched.id
    RETURNING token_row.*
)
SELECT touched.id, touched.name, touched.kind, touched.token_hash, touched.roles,
       touched.status, touched.created_by, touched.service_account_id,
       touched.created_at, touched.expires_at, touched.last_used_at, touched.revoked_at,
       sa.id AS account_id, sa.project_id, sa.owner_user_id, sa.name AS account_name,
       sa.role AS account_role, sa.status AS account_status, sa.created_at AS account_created_at,
       sa.updated_at AS account_updated_at, sa.revoked_at AS account_revoked_at
FROM touched JOIN service_accounts sa ON sa.id = touched.service_account_id;
