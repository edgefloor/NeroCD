-- name: ReadinessPlacement :one
SELECT s.id AS server_id,d.id AS destination_id,s.runner_id FROM servers s JOIN destinations d ON d.server_id=s.id WHERE s.runner_id=$1;
-- name: LockReadinessRunner :one
SELECT token_hash,status,capabilities FROM runners WHERE id=$1 FOR SHARE;
-- name: LockReadinessEnrollment :one
SELECT id FROM runner_enrollments WHERE runner_id=$1 AND used_at IS NOT NULL AND revoked_at IS NULL AND credential_hash=$2 ORDER BY id LIMIT 1 FOR SHARE;
-- name: EnsureServerReadiness :exec
INSERT INTO server_readiness(server_id) VALUES($1) ON CONFLICT DO NOTHING;
-- name: LockServerReadiness :one
SELECT * FROM server_readiness WHERE server_id=$1 FOR UPDATE;
-- name: SetServerReadinessPending :exec
UPDATE server_readiness SET pending_target=$2,pending_identity=$3 WHERE server_id=$1;
-- name: AcceptServerReadiness :exec
UPDATE server_readiness SET accepted_target=pending_target,accepted_identity=pending_identity,accepted_body=$2,accepted_ack=$3,pending_target=NULL,pending_identity='' WHERE server_id=$1;
-- name: ServerReadinessClock :one
SELECT clock_timestamp()::timestamptz AS now;
-- name: ServerReadinessSnapshot :one
SELECT s.id AS server_id,d.id AS destination_id,s.runner_id,r.token_hash,r.status,r.capabilities,
 COALESCE((SELECT e.id FROM runner_enrollments e WHERE e.runner_id=r.id AND e.used_at IS NOT NULL AND e.revoked_at IS NULL AND e.credential_hash=r.token_hash ORDER BY e.id LIMIT 1),'')::text AS enrollment_id,
 sr.accepted_identity,sr.accepted_body,sr.accepted_ack,
 EXISTS(SELECT 1 FROM resource_bindings b LEFT JOIN hosting_binding_state bs ON bs.binding_id=b.id LEFT JOIN hosting_binding_restore_holds rh ON rh.binding_id=b.id WHERE b.destination_id=d.id AND (rh.binding_id IS NOT NULL OR bs.restore_admitted=false))::boolean AS restore_held,
 EXISTS(SELECT 1 FROM resource_bindings b JOIN hosting_binding_state bs ON bs.binding_id=b.id WHERE b.destination_id=d.id AND bs.daemon_id IS NOT NULL AND bs.daemon_id IS DISTINCT FROM sr.accepted_body->>'daemon_id')::boolean AS daemon_mismatch,
 clock_timestamp()::timestamptz AS now
FROM servers s JOIN destinations d ON d.server_id=s.id JOIN runners r ON r.id=s.runner_id LEFT JOIN server_readiness sr ON sr.server_id=s.id WHERE s.id=$1;
