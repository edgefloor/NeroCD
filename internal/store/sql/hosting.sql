-- name: GetProjectEnvironment :one
SELECT * FROM project_environments WHERE id=$1;
-- name: CreateProjectEnvironment :one
INSERT INTO project_environments (id,project_id,name,created_at) VALUES ($1,$2,$3,$4) RETURNING *;
-- name: CountProjectEnvironments :one
SELECT count(*) FROM project_environments WHERE project_id=$1;
-- name: ListProjectEnvironments :many
SELECT * FROM project_environments WHERE project_id=$1 ORDER BY created_at,id LIMIT $2 OFFSET $3;
-- name: GetResource :one
SELECT * FROM resources WHERE id=$1;
-- name: CountResources :one
SELECT count(*) FROM resources WHERE project_id=$1 AND ($2::text='' OR project_environment_id=$2);
-- name: ListResources :many
SELECT * FROM resources WHERE project_id=$1 AND ($2::text='' OR project_environment_id=$2) ORDER BY created_at,id LIMIT $3 OFFSET $4;
-- name: CreateResource :one
INSERT INTO resources (id,project_id,project_environment_id,name,kind,source,generation,created_by,created_at,updated_at)
VALUES ($1,$2,$3,$4,$5,$6,1,$7,$8,$9) RETURNING *;
-- name: UpdateResource :one
UPDATE resources SET name=$2,source=$3,generation=generation+1,updated_at=$4
WHERE id=$1 AND generation=$5 AND project_id=$6 AND project_environment_id=$7 AND kind=$8 AND created_by=$9 AND created_at=$10 RETURNING *;
