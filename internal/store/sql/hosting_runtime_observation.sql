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
