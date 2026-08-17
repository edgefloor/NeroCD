-- name: CountArtifacts :one
SELECT count(*) FROM run_artifacts WHERE (sqlc.arg(run_id)::text='' OR run_id=sqlc.arg(run_id));

-- name: ListArtifactsPage :many
SELECT * FROM run_artifacts WHERE (sqlc.arg(run_id)::text='' OR run_id=sqlc.arg(run_id))
ORDER BY created_at, name LIMIT sqlc.arg(page_limit) OFFSET sqlc.arg(page_offset);

-- name: CreateArtifact :one
INSERT INTO run_artifacts (id,run_id,lease_id,name,path,found,required,size,kind,created_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING *;
