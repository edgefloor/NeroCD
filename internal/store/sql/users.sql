-- name: CountUsers :one
SELECT count(*) FROM users;

-- name: ListUsersPage :many
SELECT id, email, name, status, global_role, password_hash, created_at, updated_at, archived_at
FROM users
ORDER BY created_at DESC, id DESC
LIMIT $1 OFFSET $2;

-- name: LockIdentityAdministration :one
SELECT singleton FROM identity_bootstrap_state WHERE singleton = TRUE FOR UPDATE;

-- name: GetUserForUpdate :one
SELECT id, email, name, status, global_role, password_hash, created_at, updated_at, archived_at
FROM users
WHERE id = $1
FOR UPDATE;

-- name: CountActiveSystemAdministrators :one
SELECT count(*) FROM users WHERE status = 'active' AND global_role = 'system_admin';

-- name: DisableUser :one
UPDATE users
SET status = 'disabled', updated_at = $2, archived_at = COALESCE(archived_at, $2)
WHERE id = $1 AND status = 'active'
RETURNING id, email, name, status, global_role, password_hash, created_at, updated_at, archived_at;

-- name: RevokeSessionsForUser :execrows
UPDATE sessions SET revoked_at = $2 WHERE user_id = $1 AND revoked_at IS NULL;

-- name: RevokeAPITokensCreatedByUser :execrows
UPDATE api_tokens SET status = 'revoked', revoked_at = $2
WHERE created_by = $1 AND status = 'active' AND revoked_at IS NULL;

-- name: RevokeServiceAccountsOwnedByUser :execrows
UPDATE service_accounts SET status = 'revoked', revoked_at = $2, updated_at = $2
WHERE owner_user_id = $1 AND status = 'active';

-- name: LockActiveUser :one
SELECT id FROM users WHERE id = $1 AND status = 'active' FOR SHARE;
