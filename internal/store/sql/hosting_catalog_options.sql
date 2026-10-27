-- name: CatalogDestinationOptions :many
SELECT d.*,s.name AS server_name,r.status AS runner_status,r.capabilities,
(r.token_hash<>'' AND EXISTS(SELECT 1 FROM runner_enrollments e WHERE e.runner_id=r.id AND e.used_at IS NOT NULL AND e.revoked_at IS NULL AND e.credential_hash=r.token_hash))::boolean AS credentialed
FROM destinations d JOIN servers s ON s.id=d.server_id JOIN runners r ON r.id=s.runner_id
JOIN destination_project_grants g ON g.destination_id=d.id AND g.project_id=sqlc.arg(project_id) AND g.revoked_at IS NULL
WHERE (sqlc.arg(destination_id)::text='' OR d.id=sqlc.arg(destination_id))
ORDER BY d.created_at,d.id LIMIT sqlc.arg(page_limit) OFFSET sqlc.arg(page_offset);

-- name: LockCatalogRunner :exec
SELECT r.id FROM runners r JOIN servers s ON s.runner_id=r.id JOIN destinations d ON d.server_id=s.id WHERE d.id=$1 FOR UPDATE OF r;
