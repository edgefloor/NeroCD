-- name: CountAuditEvents :one
SELECT count(*) FROM audit_events;

-- name: ListAuditEventsPage :many
SELECT * FROM audit_events ORDER BY created_at DESC, id DESC LIMIT $1 OFFSET $2;

-- name: CreateAuditEvent :exec
INSERT INTO audit_events (id,actor_id,action,target_id,metadata,created_at)
VALUES ($1,$2,$3,$4,$5,$6);

-- name: GetAuditEventByID :one
SELECT * FROM audit_events WHERE id=$1;

-- name: CreateSecretAccessAudit :one
INSERT INTO audit_events (id,actor_id,action,target_id,metadata,created_at)
VALUES ($1,$2,'secret.access',$3,$4,clock_timestamp())
RETURNING *;

-- name: GetRunContextForSecretAccess :one
SELECT run_spec,workflow,workflow_state FROM task_runs WHERE id=$1;
