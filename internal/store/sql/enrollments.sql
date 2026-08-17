-- name: CreateRunnerEnrollment :one
INSERT INTO runner_enrollments (
  id, token_hash, runner_id, runner_name, tags, capabilities, created_by, created_at, expires_at
)
VALUES (
  sqlc.arg(id), sqlc.arg(token_hash), sqlc.arg(runner_id), sqlc.arg(runner_name),
  sqlc.arg(tags), sqlc.arg(capabilities), sqlc.arg(created_by), clock_timestamp(),
  clock_timestamp() + (sqlc.arg(ttl_microseconds)::bigint * interval '1 microsecond')
)
RETURNING *;

-- name: RevokeUnusedRunnerEnrollment :one
UPDATE runner_enrollments
SET revoked_at=clock_timestamp()
WHERE id=$1 AND revoked_at IS NULL AND used_at IS NULL
RETURNING *;

-- name: LockRunnerEnrollmentByTokenHash :one
SELECT * FROM runner_enrollments WHERE token_hash=$1 FOR UPDATE;

-- name: GetRunnerByID :one
SELECT * FROM runners WHERE id=$1;

-- name: CreateRunnerForEnrollment :one
INSERT INTO runners (id,name,tags,capabilities,status,registered_at,last_heartbeat_at,token_hash)
VALUES ($1,$2,$3,$4,'active',clock_timestamp(),clock_timestamp(),$5)
RETURNING *;

-- name: MarkRunnerEnrollmentUsed :one
UPDATE runner_enrollments
SET used_at=clock_timestamp(), consume_request_id=$2, credential_hash=$3
WHERE id=$1 AND used_at IS NULL AND revoked_at IS NULL AND expires_at > clock_timestamp()
RETURNING *;
