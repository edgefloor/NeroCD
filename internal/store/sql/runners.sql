-- name: ListRunners :many
SELECT * FROM runners ORDER BY name;

-- name: RegisterRunner :one
INSERT INTO runners (id,name,tags,capabilities,status,registered_at,last_heartbeat_at,token_hash)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
RETURNING *;

-- name: UpdateRunnerToken :one
UPDATE runners SET token_hash=$2,status=$3,last_heartbeat_at=$4 WHERE id=$1 RETURNING *;

-- name: GetRunnerByTokenHash :one
SELECT * FROM runners WHERE token_hash=$1 AND token_hash<>'' AND status='active';

-- name: HeartbeatRunner :one
UPDATE runners SET status='active',last_heartbeat_at=$2 WHERE id=$1 RETURNING *;

-- name: GetActiveRunnerForClaim :one
SELECT * FROM runners WHERE id=$1 AND status='active' AND last_heartbeat_at >= $2;
