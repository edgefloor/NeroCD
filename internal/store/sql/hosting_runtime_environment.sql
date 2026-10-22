-- name: HostingApplyAdmissionMatches :one
SELECT EXISTS(SELECT 1 FROM hosting_apply_admissions
 WHERE operation_id=sqlc.arg(operation_id) AND run_id=sqlc.arg(run_id)
 AND lease_id=sqlc.arg(lease_id) AND authority=sqlc.arg(authority)::jsonb
 AND plan_hash=sqlc.arg(plan_hash));
