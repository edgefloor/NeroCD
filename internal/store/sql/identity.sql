-- name: GetUserByEmail :one
SELECT id, email, name, status, global_role, password_hash, created_at, updated_at, archived_at
FROM users
WHERE email = $1;

-- name: CreateSession :exec
INSERT INTO sessions (id, user_id, token_hash, expires_at, created_at)
VALUES ($1, $2, $3, $4, $5);

-- name: GetPrincipalBySessionTokenHash :one
SELECT users.id, users.email, users.name, users.status, users.global_role,
       users.password_hash, users.created_at, users.updated_at, users.archived_at
FROM sessions
JOIN users ON users.id = sessions.user_id
WHERE sessions.token_hash = $1
  AND sessions.revoked_at IS NULL
  AND sessions.expires_at > $2
  AND users.status = 'active';

-- name: RevokeSessionByTokenHash :execrows
UPDATE sessions SET revoked_at = $2
WHERE token_hash = $1 AND revoked_at IS NULL;

-- name: CreateAPIToken :one
INSERT INTO api_tokens (id, name, kind, token_hash, roles, status, created_by, created_at, expires_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
RETURNING *;

-- name: GetAPITokenByHash :one
UPDATE api_tokens SET last_used_at = $2
WHERE token_hash = $1 AND status = 'active' AND revoked_at IS NULL
  AND (expires_at IS NULL OR expires_at > $2)
RETURNING *;

-- name: RevokeAPIToken :one
UPDATE api_tokens SET status = 'revoked', revoked_at = $2
WHERE id = $1 AND revoked_at IS NULL
RETURNING *;
