-- name: ListApprovals :many
SELECT * FROM approvals WHERE (sqlc.arg(status)::text='' OR status=sqlc.arg(status)) ORDER BY created_at DESC;

-- name: CreateApproval :one
INSERT INTO approvals (id,run_id,status,requested_by,approved_by,created_at,approved_at)
VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING *;

-- name: ResolveApproval :one
UPDATE approvals SET status=$2,approved_by=$3,approved_at=$4
WHERE run_id=$1 AND status='pending' RETURNING *;
