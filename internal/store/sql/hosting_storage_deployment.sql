-- name: StorageDeploymentResource :one
SELECT r.id,r.project_id,r.kind,r.source,r.generation FROM resources r JOIN projects p ON p.id=r.project_id WHERE r.id=$1 AND p.archived_at IS NULL;

-- name: StorageDeploymentRuntimeGeneration :one
SELECT coalesce((SELECT generation FROM hosting_runtime_configurations WHERE resource_id=$1),0)::bigint AS generation;

-- name: StorageDeploymentBindingState :one
SELECT healthy,restore_admitted,hold_operation_id,daemon_id FROM hosting_binding_state WHERE binding_id=$1;

-- name: StorageDeploymentRunnerEligible :one
SELECT EXISTS(SELECT 1 FROM runners r JOIN servers s ON s.runner_id=r.id JOIN destinations d ON d.server_id=s.id JOIN resource_bindings b ON b.destination_id=d.id
WHERE b.id=sqlc.arg(binding_id)::text AND b.resource_id=sqlc.arg(resource_id)::text AND d.id=sqlc.arg(destination_id)::text AND s.id=sqlc.arg(server_id)::text AND r.id=sqlc.arg(runner_id)::text
AND r.status='active' AND r.token_hash<>'' AND NOT('shell'=ANY(r.capabilities)) AND sqlc.arg(image_capability)::text=ANY(r.capabilities) AND sqlc.arg(storage_capability)::text=ANY(r.capabilities) AND sqlc.arg(release_capability)::text=ANY(r.capabilities)
AND EXISTS(SELECT 1 FROM runner_enrollments e WHERE e.runner_id=r.id AND e.used_at IS NOT NULL AND e.revoked_at IS NULL AND e.credential_hash=r.token_hash));

-- name: StorageDeploymentContinuitySuccessor :one
SELECT successor_proxy_operation_id FROM hosting_storage_route_continuities WHERE operation_id=$1;

-- name: StorageDeploymentInitialHistory :one
SELECT EXISTS(SELECT 1 FROM hosting_operations WHERE resource_id=sqlc.arg(resource_id)::text OR binding_id=sqlc.arg(binding_id)::text)
OR EXISTS(SELECT 1 FROM hosting_storage_materializations WHERE storage_id=sqlc.arg(storage_id)::text)
OR EXISTS(SELECT 1 FROM hosting_storage_operation_admissions WHERE storage_id=sqlc.arg(storage_id)::text)
OR EXISTS(SELECT 1 FROM hosting_retained_releases WHERE binding_id=sqlc.arg(binding_id)::text)
OR EXISTS(SELECT 1 FROM hosting_release_claims WHERE binding_id=sqlc.arg(binding_id)::text)
OR EXISTS(SELECT 1 FROM hosting_release_slots WHERE binding_id=sqlc.arg(binding_id)::text)
OR EXISTS(SELECT 1 FROM hosting_proxy_routes WHERE resource_id=sqlc.arg(resource_id)::text OR binding_id=sqlc.arg(binding_id)::text)
AS recorded;

-- name: StorageDeploymentRestoredInitialHistory :one
SELECT EXISTS(SELECT 1 FROM hosting_operations WHERE (resource_id=sqlc.arg(resource_id)::text OR binding_id=sqlc.arg(binding_id)::text) AND id<>sqlc.arg(restore_operation_id)::text)
OR EXISTS(SELECT 1 FROM hosting_storage_materializations WHERE storage_id=sqlc.arg(storage_id)::text)
OR EXISTS(SELECT 1 FROM hosting_storage_operation_admissions WHERE storage_id=sqlc.arg(storage_id)::text)
OR EXISTS(SELECT 1 FROM hosting_storage_restore_use_admissions WHERE storage_id=sqlc.arg(storage_id)::text)
OR EXISTS(SELECT 1 FROM hosting_retained_releases WHERE binding_id=sqlc.arg(binding_id)::text)
OR EXISTS(SELECT 1 FROM hosting_release_claims WHERE binding_id=sqlc.arg(binding_id)::text)
-- Restore admission reserves precisely this binding-state port before any writer.
OR EXISTS(SELECT 1 FROM hosting_release_slots s WHERE s.binding_id=sqlc.arg(binding_id)::text
 AND NOT EXISTS(SELECT 1 FROM hosting_binding_state b WHERE b.binding_id=s.binding_id AND b.destination_id=s.destination_id AND b.host_port=s.host_port))
OR EXISTS(SELECT 1 FROM hosting_proxy_routes WHERE resource_id=sqlc.arg(resource_id)::text OR binding_id=sqlc.arg(binding_id)::text)
AS recorded;
