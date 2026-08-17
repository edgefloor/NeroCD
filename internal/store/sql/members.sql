-- name: ListProjectMembers :many
SELECT members.id, members.project_id, members.user_id, users.email, users.name,
       members.role, members.created_at, members.updated_at
FROM project_members AS members
JOIN users ON users.id=members.user_id
WHERE (sqlc.arg(project_id)::text='' OR members.project_id=sqlc.arg(project_id))
ORDER BY members.project_id, users.email;

-- name: UpsertProjectMember :one
INSERT INTO project_members (id,project_id,user_id,role,created_at,updated_at)
VALUES ($1,$2,$3,$4,$5,$6)
ON CONFLICT (project_id,user_id) DO UPDATE SET role=excluded.role, updated_at=excluded.updated_at
RETURNING *;

-- name: GetUserByID :one
SELECT * FROM users WHERE id=$1;
