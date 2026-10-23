-- name: BootstrapClock :one
SELECT clock_timestamp()::timestamptz;
-- name: BootstrapSerialize :exec
SELECT pg_advisory_xact_lock(hashtextextended(sqlc.arg(identity)::text,612025));
-- name: BootstrapCreate :one
INSERT INTO server_bootstrap_operations(id,project_id,actor_id,request_key,request_hash,target,body,version,state) VALUES($1,$2,$3,$4,$5,$6,$7,1,'draft') RETURNING *;
-- name: BootstrapGet :one
SELECT * FROM server_bootstrap_operations WHERE id=$1;
-- name: BootstrapLock :one
SELECT * FROM server_bootstrap_operations WHERE id=$1 FOR UPDATE;
-- name: BootstrapFindRequest :one
SELECT * FROM server_bootstrap_operations WHERE actor_id=$1 AND project_id=$2 AND request_key=$3;
-- name: BootstrapUpdate :one
UPDATE server_bootstrap_operations SET version=version+1,state=$2,remote_hold=remote_hold OR $3 WHERE id=$1 RETURNING *;
-- name: BootstrapCountCommands :one
SELECT count(*) FROM server_bootstrap_commands WHERE operation_id=$1 AND action<>'inspect_owned';
-- name: BootstrapCommandCreate :one
INSERT INTO server_bootstrap_commands(id,operation_id,actor_id,request_key,request_hash,action,body,deadline,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING *;
-- name: BootstrapCommandRequest :one
SELECT * FROM server_bootstrap_commands WHERE operation_id=$1 AND request_key=$2;
-- name: BootstrapCommandLock :one
SELECT * FROM server_bootstrap_commands WHERE operation_id=$1 AND id=$2 FOR UPDATE;
-- name: BootstrapLatestReview :one
SELECT body FROM server_bootstrap_commands WHERE operation_id=$1 AND action='review' AND receipt IS NOT NULL ORDER BY created_at DESC,id DESC LIMIT 1;
-- name: BootstrapCommandLease :one
UPDATE server_bootstrap_commands SET lease_id=$2,lease_expires_at=$3,attempt=attempt+1,fence=fence+1 WHERE id=$1 RETURNING *;
-- name: BootstrapCommandRenew :one
UPDATE server_bootstrap_commands SET lease_expires_at=$2 WHERE id=$1 RETURNING *;
-- name: BootstrapCommandIntent :exec
UPDATE server_bootstrap_commands SET effect_intent=true WHERE id=$1;
-- name: BootstrapCommandReceipt :exec
UPDATE server_bootstrap_commands SET receipt=$2 WHERE id=$1 AND receipt IS NULL;
-- name: BootstrapGrantCreate :exec
INSERT INTO server_bootstrap_secret_grants(operation_id,principal_id,purpose,target_hash,secret_id,secret_version,expires_at) VALUES($1,$2,'ssh_bootstrap',$3,$4,$5,$6);
-- name: BootstrapGrantLock :one
SELECT * FROM server_bootstrap_secret_grants WHERE operation_id=$1 FOR UPDATE;
-- name: BootstrapCredential :one
SELECT * FROM server_bootstrap_enrollment_credentials WHERE operation_id=$1;
-- name: BootstrapCredentialCreate :exec
INSERT INTO server_bootstrap_enrollment_credentials(operation_id,enrollment_id,envelope_version,key_id,ciphertext) VALUES($1,$2,$3,$4,$5);
-- name: BootstrapEnrollmentLock :one
SELECT * FROM runner_enrollments WHERE id=$1 FOR UPDATE;
-- name: BootstrapRestoreHold :exec
UPDATE server_bootstrap_operations SET restore_held=true;
-- name: BootstrapRestoreGrants :exec
UPDATE server_bootstrap_secret_grants SET revoked_at=COALESCE(revoked_at,clock_timestamp());
-- name: BootstrapRestoreLeases :exec
UPDATE server_bootstrap_commands SET lease_expires_at=LEAST(lease_expires_at,clock_timestamp()) WHERE receipt IS NULL;
-- name: BootstrapRestoreEnrollments :exec
UPDATE runner_enrollments SET revoked_at=COALESCE(revoked_at,clock_timestamp()) WHERE used_at IS NULL AND id IN(SELECT enrollment_id FROM server_bootstrap_enrollment_credentials);
-- name: BootstrapExpiredIntent :one
SELECT EXISTS(SELECT 1 FROM server_bootstrap_commands WHERE operation_id=$1 AND effect_intent AND receipt IS NULL AND lease_expires_at<=clock_timestamp())::boolean;
-- name: BootstrapRevokeGrant :exec
UPDATE server_bootstrap_secret_grants SET revoked_at=COALESCE(revoked_at,clock_timestamp()) WHERE operation_id=$1;
-- name: BootstrapCommandHistory :many
SELECT * FROM server_bootstrap_commands WHERE operation_id=$1 ORDER BY created_at,id LIMIT 41;
-- name: BootstrapCountInspections :one
SELECT count(*) FROM server_bootstrap_commands WHERE operation_id=$1 AND action='inspect_owned';
-- name: BootstrapHasEffectIntent :one
SELECT EXISTS(SELECT 1 FROM server_bootstrap_commands WHERE operation_id=$1 AND effect_intent)::boolean;
-- name: BootstrapRetireReadStages :exec
UPDATE server_bootstrap_commands AS c SET receipt=jsonb_build_object('command_id',c.id,'lease_id',c.lease_id,'attempt',c.attempt,'fence',c.fence,'outcome','refused'),lease_id='',lease_expires_at=clock_timestamp(),fence=c.fence+1 WHERE c.operation_id=$1 AND c.receipt IS NULL AND NOT c.effect_intent AND c.action IN ('probe','inspect_owned');
-- name: BootstrapRevokeUnusedEnrollment :exec
UPDATE runner_enrollments SET revoked_at=COALESCE(revoked_at,clock_timestamp()) WHERE used_at IS NULL AND id=(SELECT o.body->'ids'->>'enrollment_id' FROM server_bootstrap_operations o WHERE o.id=$1);
-- name: BootstrapReadLock :one
SELECT * FROM server_bootstrap_operations WHERE id=$1 FOR SHARE;
