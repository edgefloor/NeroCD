-- name: ListProjects :many
SELECT * FROM projects WHERE archived_at IS NULL ORDER BY name;

-- name: CreateProject :one
INSERT INTO projects (id,name,description,created_at)
VALUES ($1,$2,$3,$4) RETURNING *;

-- name: UpdateProject :one
UPDATE projects SET name=$2, description=$3, updated_at=now()
WHERE id=$1 AND archived_at IS NULL RETURNING *;

-- name: ArchiveProject :one
UPDATE projects SET archived_at=$2, updated_at=now()
WHERE id=$1 AND archived_at IS NULL RETURNING *;
