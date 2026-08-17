-- name: ListTemplates :many
SELECT * FROM task_templates
WHERE (sqlc.arg(project_id)::text='' OR project_id=sqlc.arg(project_id))
ORDER BY name;

-- name: GetTemplate :one
SELECT * FROM task_templates WHERE id=$1;

-- name: CreateTemplate :one
INSERT INTO task_templates (id,project_id,name,kind,run_spec,workflow,runner_tags,requires_ack)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING *;

-- name: UpdateTemplate :one
UPDATE task_templates SET name=$2, kind=$3, run_spec=$4, workflow=$5,
 runner_tags=$6, requires_ack=$7, updated_at=now()
WHERE id=$1 RETURNING *;
