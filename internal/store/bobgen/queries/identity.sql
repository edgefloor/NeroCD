-- PrincipalBySessionTokenHash
SELECT
    users.id,
    users.email,
    users.name,
    users.status,
    users.global_role,
    users.password_hash,
    users.created_at
FROM sessions
JOIN users ON users.id = sessions.user_id
WHERE sessions.token_hash = $1
  AND sessions.expires_at > $2
  AND sessions.revoked_at IS NULL
  AND users.status = 'active';
