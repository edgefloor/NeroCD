-- ActiveProjects
SELECT id, name, description, created_at, archived_at
FROM projects
WHERE archived_at IS NULL
ORDER BY name ASC;

-- ProjectByID
SELECT id, name, description, created_at, archived_at
FROM projects
WHERE id = $1 AND archived_at IS NULL;
