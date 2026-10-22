-- name: CountManagedSecrets :one
SELECT count(*) FROM project_managed_secrets WHERE project_id=$1;
-- name: ListManagedSecrets :many
SELECT * FROM project_managed_secrets WHERE project_id=$1 ORDER BY created_at,id LIMIT $2 OFFSET $3;
-- name: GetManagedSecret :one
SELECT * FROM project_managed_secrets WHERE id=$1;
-- name: LockManagedSecret :one
SELECT * FROM project_managed_secrets WHERE id=$1 FOR UPDATE;
-- name: CountManagedSecretVersions :one
SELECT count(*) FROM project_managed_secret_versions WHERE secret_id=$1;
-- name: ListManagedSecretVersions :many
SELECT secret_id,version,created_by,created_at FROM project_managed_secret_versions WHERE secret_id=$1 ORDER BY version DESC LIMIT $2 OFFSET $3;
-- name: GetManagedSecretRecord :one
SELECT * FROM project_managed_secret_versions WHERE secret_id=$1 AND version=$2;
-- name: CreateManagedSecret :one
INSERT INTO project_managed_secrets(id,project_id,name,current_version,status,created_by,created_at,updated_at) VALUES($1,$2,$3,1,'active',$4,$5,$5) RETURNING *;
-- name: AppendManagedSecretVersion :exec
INSERT INTO project_managed_secret_versions(secret_id,version,envelope_version,key_id,ciphertext,created_by,created_at) VALUES($1,$2,$3,$4,$5,$6,$7);
-- name: AdvanceManagedSecret :one
UPDATE project_managed_secrets SET current_version=current_version+1,updated_at=$2 WHERE id=$1 AND status='active' AND current_version=$3 AND current_version<9223372036854775807 RETURNING *;
-- name: RevokeManagedSecret :one
UPDATE project_managed_secrets SET status='revoked',revoked_at=$2,updated_at=$2 WHERE id=$1 AND status='active' AND current_version=$3 RETURNING *;
