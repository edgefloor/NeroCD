-- name: LockOIDCLoginFlows :exec
SELECT pg_advisory_xact_lock(704320031);

-- name: DeleteExpiredOIDCLoginFlows :execrows
DELETE FROM oidc_login_flows AS expired
WHERE expired.id IN (
  SELECT candidate.id FROM oidc_login_flows AS candidate
  WHERE candidate.expires_at <= $1
  ORDER BY candidate.expires_at, candidate.id
  LIMIT $2
);

-- name: CountActiveOIDCLoginFlows :one
SELECT count(*) FROM oidc_login_flows WHERE expires_at > $1;

-- name: CreateOIDCLoginFlow :exec
INSERT INTO oidc_login_flows
  (id, state_hash, nonce_hash, verifier_hash, redirect_path, issuer, client_id, expires_at, created_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9);

-- name: ConsumeOIDCLoginFlow :one
DELETE FROM oidc_login_flows
WHERE state_hash = $1
  AND verifier_hash = $2
  AND issuer = $3
  AND client_id = $4
  AND expires_at > $5
RETURNING id, state_hash, nonce_hash, verifier_hash, redirect_path, issuer, client_id, expires_at, created_at;

-- name: CreateOIDCUser :exec
INSERT INTO users (id, email, name, status, global_role, password_hash, created_at, updated_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$7);

-- name: CreateOIDCIdentity :exec
INSERT INTO oidc_external_identities (issuer, subject, user_id, created_at)
VALUES ($1,$2,$3,$4);

-- name: GetOIDCUser :one
SELECT users.id, users.email, users.name, users.status, users.global_role,
       users.password_hash, users.created_at, users.updated_at, users.archived_at
FROM oidc_external_identities AS identity
JOIN users ON users.id = identity.user_id
WHERE identity.issuer = $1 AND identity.subject = $2
FOR SHARE OF users;
