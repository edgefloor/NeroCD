-- name: GetUserByEmail :one
SELECT id, email, name, status, global_role, password_hash, created_at, updated_at, archived_at
FROM users
WHERE email = $1;

-- name: UpdatePasswordHash :execrows
UPDATE users SET password_hash = $2, updated_at = clock_timestamp()
WHERE id = $1 AND password_hash = $3;

-- name: ClaimBootstrapAdmin :execrows
UPDATE identity_bootstrap_state
SET completed_by = $1, completed_at = $2
WHERE singleton = TRUE AND completed_by IS NULL;

-- name: BootstrapComplete :one
SELECT (completed_by IS NOT NULL)::boolean AS complete
FROM identity_bootstrap_state
WHERE singleton = TRUE;

-- name: CreateBootstrapUser :exec
INSERT INTO users (id, email, name, status, global_role, password_hash, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $7);

-- name: CreateSession :exec
INSERT INTO sessions (id, user_id, token_hash, expires_at, created_at, source_ip, user_agent, last_seen_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

-- name: GetPrincipalBySessionTokenHash :one
WITH matched AS (
  SELECT matched_session.user_id
  FROM sessions AS matched_session
  WHERE matched_session.token_hash = $1 AND matched_session.revoked_at IS NULL AND matched_session.expires_at > $2
), touched AS (
  UPDATE sessions AS session_row SET last_seen_at = $2
  WHERE session_row.token_hash = $1 AND session_row.revoked_at IS NULL AND session_row.expires_at > $2
    AND (session_row.last_seen_at IS NULL OR session_row.last_seen_at <= $3)
  RETURNING user_id
)
SELECT users.id, users.email, users.name, users.status, users.global_role,
       users.password_hash, users.created_at, users.updated_at, users.archived_at
FROM matched
JOIN users ON users.id = matched.user_id
WHERE users.status = 'active';

-- name: RevokeSessionByTokenHash :execrows
UPDATE sessions SET revoked_at = $2
WHERE token_hash = $1 AND revoked_at IS NULL;

-- name: ListSessions :many
SELECT id, user_id, expires_at, created_at, source_ip, user_agent, last_seen_at, revoked_at
FROM sessions
ORDER BY created_at DESC, id DESC;

-- name: RevokeSessionByID :one
UPDATE sessions SET revoked_at = $2
WHERE id = $1 AND revoked_at IS NULL
RETURNING id, user_id, expires_at, created_at, source_ip, user_agent, last_seen_at, revoked_at;

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
