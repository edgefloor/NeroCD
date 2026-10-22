-- name: EnsureHostingRuntimeObservation :exec
INSERT INTO hosting_runtime_observations(binding_id) VALUES($1) ON CONFLICT DO NOTHING;
-- name: LockHostingRuntimeObservation :one
SELECT * FROM hosting_runtime_observations WHERE binding_id=$1 FOR UPDATE;
-- name: SetHostingRuntimeObservationPending :exec
UPDATE hosting_runtime_observations SET pending_target=$2,pending_identity=$3 WHERE binding_id=$1;
-- name: AcceptHostingRuntimeObservation :exec
UPDATE hosting_runtime_observations SET accepted_target=pending_target,accepted_identity=pending_identity,accepted_body=$2,accepted_ack=$3,pending_target=NULL,pending_identity='' WHERE binding_id=$1;
-- name: HostingObservationClock :one
SELECT clock_timestamp()::timestamptz AS now;

-- name: ObservationResourceDestination :one
SELECT r.project_id,b.destination_id FROM resources r LEFT JOIN resource_bindings b ON b.resource_id=r.id WHERE r.id=$1;
-- name: LockObservationDestinationRow :one
SELECT id FROM destinations WHERE id=$1 FOR SHARE;
-- name: LockObservationBindingPlacement :one
SELECT b.id,b.destination_id,b.runtime_namespace,d.server_id,s.runner_id FROM resource_bindings b JOIN destinations d ON d.id=b.destination_id JOIN servers s ON s.id=d.server_id WHERE b.resource_id=$1 FOR SHARE OF b,s;
-- name: LockObservationGrant :one
SELECT revoked_at,created_at FROM destination_project_grants WHERE destination_id=$1 AND project_id=$2 FOR UPDATE;
-- name: LockObservationSettledState :one
SELECT healthy,hold_operation_id,restore_admitted FROM hosting_binding_state WHERE binding_id=$1 FOR UPDATE;
-- name: LockObservationRunner :one
SELECT token_hash,last_heartbeat_at,status,capabilities FROM runners WHERE id=$1 FOR SHARE;
-- name: LockObservationEnrollment :one
SELECT id FROM runner_enrollments WHERE runner_id=$1 AND used_at IS NOT NULL AND revoked_at IS NULL AND credential_hash=$2 ORDER BY id LIMIT 1 FOR SHARE;
-- name: ListObservationBindingCandidates :many
SELECT b.id,b.resource_id FROM resource_bindings b JOIN destinations d ON d.id=b.destination_id JOIN servers s ON s.id=d.server_id JOIN hosting_binding_state bs ON bs.binding_id=b.id WHERE s.runner_id=$1 AND b.id>sqlc.arg(after_binding_id)::text AND bs.healthy IS NOT NULL ORDER BY b.id LIMIT sqlc.arg(page_limit)::integer;
-- name: ObservationBindingResource :one
SELECT resource_id FROM resource_bindings WHERE id=$1;
