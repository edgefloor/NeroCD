-- name: RecoverQueuedHostingOperations :many
SELECT id FROM hosting_operations WHERE state='queued' AND run_id IS NULL ORDER BY updated_at,id LIMIT 100 FOR UPDATE SKIP LOCKED;

-- name: TouchHostingOperation :execrows
UPDATE hosting_operations SET updated_at=clock_timestamp() WHERE id=$1;

-- name: GetHostingOperationSnapshot :one
SELECT o.id,o.project_id,o.resource_id,o.binding_id,coalesce(o.run_id,'') AS run_id,coalesce(o.reconciliation_run_id,'') AS reconciliation_run_id,o.state,o.reconcile_requested,o.created_by,o.created_at,o.updated_at,
 sn.body AS snapshot,rel.body AS release,plan.body AS plan,
 (SELECT plan FROM hosting_cleanup_targets WHERE operation_id=o.id) AS cleanup_plan,
 (SELECT jsonb_build_object('id',receipt_id,'body_hash',body_hash,'created_at',created_at) FROM hosting_receipts WHERE operation_id=o.id AND phase='prepared') AS prepared_receipt,
 (SELECT jsonb_build_object('id',receipt_id,'body_hash',body_hash,'created_at',created_at) FROM hosting_receipts WHERE operation_id=o.id AND phase='complete') AS complete_receipt,
 (SELECT body FROM hosting_receipts WHERE operation_id=o.id AND phase='complete') AS result,
 EXISTS(SELECT 1 FROM hosting_binding_state bs WHERE bs.hold_operation_id=o.id) AS mutation_hold,
 (EXISTS(SELECT 1 FROM hosting_binding_state bs WHERE bs.binding_id=o.binding_id AND NOT bs.restore_admitted) OR EXISTS(SELECT 1 FROM hosting_binding_restore_holds rh WHERE rh.binding_id=o.binding_id)) AS restore_ownership_held
 FROM hosting_operations o JOIN hosting_snapshots sn ON sn.operation_id=o.id
 LEFT JOIN hosting_releases rel ON rel.operation_id=o.id LEFT JOIN hosting_apply_plans plan ON plan.operation_id=o.id WHERE o.id=$1;

-- name: LockHostingResourceTarget :one
SELECT r.project_id,r.generation,r.source,b.id AS binding_id,b.destination_id,b.runtime_namespace,d.server_id,s.runner_id
 FROM resources r JOIN resource_bindings b ON b.resource_id=r.id JOIN destinations d ON d.id=b.destination_id JOIN servers s ON s.id=d.server_id
 WHERE r.id=$1 FOR UPDATE OF r;

-- name: GetHostingIdempotency :one
SELECT id,request_hash FROM hosting_operations WHERE resource_id=$1 AND idempotency_key=$2;

-- name: CreateHostingPortReservation :execrows
INSERT INTO hosting_binding_state(binding_id,destination_id,host_port) VALUES($1,$2,$3) ON CONFLICT(binding_id) DO NOTHING;

-- name: LockHostingPortReservation :one
SELECT host_port,restore_admitted,hold_operation_id FROM hosting_binding_state WHERE binding_id=$1 FOR UPDATE;

-- name: HostingBindingBusy :one
SELECT EXISTS(SELECT 1 FROM hosting_operations WHERE binding_id=$1 AND state NOT IN ('succeeded','failed','canceled'));

-- name: CreateHostingOperation :execrows
INSERT INTO hosting_operations(id,project_id,resource_id,binding_id,idempotency_key,request_hash,state,created_by) VALUES($1,$2,$3,$4,$5,$6,'queued',$7);

-- name: CreateHostingSnapshot :execrows
INSERT INTO hosting_snapshots(operation_id,body,body_hash) VALUES($1,$2,$3);

-- name: LockHostingProjectGrant :one
SELECT revoked_at FROM destination_project_grants WHERE destination_id=$1 AND project_id=$2 FOR UPDATE;

-- name: HostingBindingRestoreHeld :one
SELECT EXISTS(SELECT 1 FROM hosting_binding_restore_holds WHERE binding_id=$1);

-- name: LockHostingAdmissionState :one
SELECT restore_admitted,hold_operation_id FROM hosting_binding_state WHERE binding_id=$1 FOR UPDATE;

-- name: LockHostingExactRunner :one
SELECT r.id FROM runners r JOIN servers s ON s.runner_id=r.id JOIN destinations d ON d.server_id=s.id JOIN resource_bindings b ON b.destination_id=d.id JOIN resources res ON res.id=b.resource_id JOIN projects p ON p.id=res.project_id
 WHERE b.id=$1 AND b.resource_id=$2 AND d.id=$3 AND s.id=$4 AND r.id=$5 AND res.project_id=$6 AND p.archived_at IS NULL
 AND r.status='active' AND r.token_hash<>'' AND 'image_deploy'=ANY(r.capabilities) AND NOT('shell'=ANY(r.capabilities))
 AND EXISTS(SELECT 1 FROM runner_enrollments e WHERE e.runner_id=r.id AND e.used_at IS NOT NULL AND e.revoked_at IS NULL AND e.credential_hash=r.token_hash)
 FOR SHARE OF r;

-- name: FailQueuedHostingOperation :execrows
UPDATE hosting_operations SET state='failed',updated_at=clock_timestamp() WHERE id=$1 AND state='queued';

-- name: LockHostingOperationState :one
SELECT state FROM hosting_operations WHERE id=$1 FOR UPDATE;

-- name: CreateHostingTask :execrows
INSERT INTO task_runs(id,project_id,run_spec,workflow,workflow_state,runner_tags,status,requested_by,started_at,created_at,claim_order_at) VALUES($1,$2,$3,'{}','{}','{}','queued',$4,clock_timestamp(),clock_timestamp(),clock_timestamp());

-- name: CreateHostingDeploymentTarget :execrows
INSERT INTO hosting_task_targets(run_id,operation_id,binding_id,runner_id,mode) VALUES($1,$2,$3,$4,'deploy');

-- name: CreateHostingDispatchReceipt :execrows
INSERT INTO hosting_dispatch_receipts(operation_id,run_id) VALUES($1,$2);

-- name: DispatchHostingOperation :execrows
UPDATE hosting_operations SET state='dispatched',run_id=$2,updated_at=clock_timestamp() WHERE id=$1;

-- name: LockHostingTaskForAuthority :execrows
SELECT id FROM task_runs WHERE id=$1 FOR UPDATE;

-- name: CheckHostingAuthority :one
SELECT l.id FROM run_leases l JOIN hosting_task_targets t ON t.run_id=l.run_id WHERE l.id=$1 AND l.run_id=$2 AND l.runner_id=$3 AND l.attempt=$4 AND l.fence=$5 AND t.operation_id=$6 AND t.runner_id=$3 AND (NOT $7::boolean OR (l.status='active' AND l.expires_at>clock_timestamp())) FOR UPDATE OF l;

-- name: GetHostingTaskMode :one
SELECT mode FROM hosting_task_targets WHERE run_id=$1;

-- name: GetHostingHealthy :one
SELECT healthy FROM hosting_binding_state WHERE binding_id=$1;

-- name: BeginHostingPreparation :execrows
UPDATE hosting_operations SET state=CASE WHEN state='dispatched' THEN 'preparing' ELSE state END,updated_at=clock_timestamp() WHERE id=$1;

-- name: GetHostingReceipt :one
SELECT receipt_id,body_hash,authority FROM hosting_receipts WHERE operation_id=$1 AND phase=$2;

-- name: CreateHostingReceipt :execrows
INSERT INTO hosting_receipts(operation_id,phase,receipt_id,authority,body,body_hash) VALUES($1,$2,$3,$4,$5,$6);

-- name: GetHostingPreparedBinding :one
SELECT daemon_id,healthy,hold_operation_id FROM hosting_binding_state WHERE binding_id=$1;

-- name: ObserveHostingDaemon :execrows
UPDATE hosting_binding_state SET daemon_id=coalesce(daemon_id,$2) WHERE binding_id=$1;

-- name: CreateHostingRelease :execrows
INSERT INTO hosting_releases(operation_id,body,body_hash) VALUES($1,$2,$3);

-- name: CreateHostingApplyPlan :execrows
INSERT INTO hosting_apply_plans(operation_id,body,body_hash) VALUES($1,$2,$3);

-- name: FinishHostingPreparation :execrows
UPDATE hosting_operations SET state='prepared',updated_at=clock_timestamp() WHERE id=$1;

-- name: AdmitHostingMutation :execrows
UPDATE hosting_binding_state SET hold_operation_id=$2 WHERE binding_id=$1 AND hold_operation_id IS NULL AND restore_admitted AND daemon_id=$3;

-- name: CreateHostingApplyAdmission :execrows
INSERT INTO hosting_apply_admissions(operation_id,run_id,lease_id,authority,plan_hash) VALUES($1,$2,$3,$4,$5);

-- name: BeginHostingApply :execrows
UPDATE hosting_operations SET state='applying',updated_at=clock_timestamp() WHERE id=$1;

-- name: HostingLeaseLive :one
SELECT status='active' AND expires_at>clock_timestamp() FROM run_leases WHERE id=$1;

-- name: SetHostingOperationState :execrows
UPDATE hosting_operations SET state=$2,updated_at=clock_timestamp() WHERE id=$1;

-- name: CompleteHostingLease :execrows
UPDATE run_leases SET status=$2,completed_at=clock_timestamp(),completion_key=$3 WHERE id=$1;

-- name: CompleteHostingTask :execrows
UPDATE task_runs SET status=$2,finished_at=clock_timestamp() WHERE id=$1;

-- name: SetHostingHealthy :execrows
UPDATE hosting_binding_state SET healthy=$2 WHERE binding_id=$1 AND hold_operation_id=$3;

-- name: ClearHostingMutationHold :execrows
UPDATE hosting_binding_state SET hold_operation_id=NULL WHERE binding_id=$1 AND hold_operation_id=$2;

-- name: LockHostingMutationHold :one
SELECT hold_operation_id FROM hosting_binding_state WHERE binding_id=$1 FOR UPDATE;

-- name: LockHostingOperation :one
SELECT id FROM hosting_operations WHERE id=$1 FOR UPDATE;

-- name: CreateHostingObservationTarget :execrows
INSERT INTO hosting_task_targets(run_id,operation_id,binding_id,runner_id,mode) VALUES($1,$2,$3,$4,'observe');

-- name: RequeueHostingObservation :execrows
UPDATE task_runs SET status='queued',runner_id=NULL,finished_at=NULL,claim_order_at=clock_timestamp() WHERE id=$1;

-- name: RequestHostingObservation :execrows
UPDATE hosting_operations SET reconcile_requested=true,reconciliation_run_id=$2,updated_at=clock_timestamp() WHERE id=$1;

-- name: CancelHostingOperationState :execrows
UPDATE hosting_operations SET state=$2,reconcile_requested=false,updated_at=clock_timestamp() WHERE id=$1;

-- name: CancelHostingLeases :execrows
UPDATE run_leases SET status='canceled',completed_at=clock_timestamp() WHERE run_id=$1 AND status='active';

-- name: CancelHostingTask :execrows
UPDATE task_runs SET status='canceled',finished_at=clock_timestamp() WHERE id=$1;

-- name: LockHostingTask :one
SELECT id FROM task_runs WHERE id=$1 FOR UPDATE;

-- name: GetHostingReconciliationReceipt :one
SELECT body_hash FROM hosting_reconciliation_receipts WHERE lease_id=$1 AND operation_id=$2;

-- name: GetHostingObservationBinding :one
SELECT daemon_id,healthy,restore_admitted FROM hosting_binding_state WHERE binding_id=$1;

-- name: HostingAdmissionExists :one
SELECT EXISTS(SELECT 1 FROM hosting_apply_admissions WHERE operation_id=$1);

-- name: HostingDeploymentLeaseLive :one
SELECT EXISTS(SELECT 1 FROM run_leases WHERE run_id=$1 AND status='active' AND expires_at>clock_timestamp());

-- name: SettleHostingObservedHealthy :execrows
UPDATE hosting_binding_state SET healthy=$2,hold_operation_id=NULL WHERE binding_id=$1 AND hold_operation_id=$3;

-- name: ClearHostingObservationRequest :execrows
UPDATE hosting_operations SET reconcile_requested=false,updated_at=clock_timestamp() WHERE id=$1;

-- name: CreateHostingReconciliationReceipt :execrows
INSERT INTO hosting_reconciliation_receipts(lease_id,operation_id,body,body_hash) VALUES($1,$2,$3,$4);

-- name: CompleteHostingObservationLease :execrows
UPDATE run_leases SET status='succeeded',completed_at=clock_timestamp(),completion_key=$2 WHERE id=$1;

-- name: CompleteHostingObservationTask :execrows
UPDATE task_runs SET status='succeeded',finished_at=clock_timestamp() WHERE id=$1;

-- name: GetHostingTargetMode :one
SELECT operation_id,mode FROM hosting_task_targets WHERE run_id=$1 AND runner_id=$2;

-- name: CheckHostingTaskMode :one
SELECT mode FROM hosting_task_targets WHERE run_id=$1 AND operation_id=$2;

-- name: BeginHostingVerification :execrows
UPDATE hosting_operations SET state='verifying',updated_at=clock_timestamp() WHERE id=$1;

-- name: ExpireHostingObservationLease :execrows
UPDATE run_leases SET status='expired',completed_at=clock_timestamp() WHERE run_id=$1 AND status='active' AND expires_at<=clock_timestamp();

-- name: ResetHostingObservation :execrows
UPDATE task_runs SET status='queued',runner_id=NULL,finished_at=NULL WHERE id=$1;

-- name: CountHostingOperations :one
SELECT count(*) FROM hosting_operations WHERE resource_id=$1;

-- name: ListHostingOperationIDs :many
SELECT id FROM hosting_operations WHERE resource_id=$1 ORDER BY created_at DESC,id DESC LIMIT $2 OFFSET $3;

-- name: HostingResourceDestination :one
SELECT b.destination_id FROM resource_bindings b WHERE b.resource_id=$1;

-- name: HostingRunnerHasReleasePorts :one
SELECT sqlc.arg(capability)::text=ANY(capabilities) AS capable FROM runners WHERE id=sqlc.arg(runner_id);

-- name: LockHostingExecutionDestination :exec
SELECT pg_advisory_xact_lock(hashtextextended('nerocd-hosting-destination:'||$1::text,0));

-- name: PickHostingReleasePort :one
SELECT port::integer AS host_port FROM generate_series(sqlc.arg(min_port)::integer,sqlc.arg(max_port)::integer) port
WHERE NOT EXISTS(SELECT 1 FROM hosting_release_slots s WHERE s.destination_id=sqlc.arg(destination_id) AND s.host_port=port)
AND NOT EXISTS(SELECT 1 FROM hosting_binding_state s WHERE s.destination_id=sqlc.arg(destination_id) AND s.host_port=port)
AND NOT EXISTS(SELECT 1 FROM hosting_proxies p WHERE p.destination_id=sqlc.arg(destination_id) AND (p.configuration->>'management_port')::integer=port)
ORDER BY port LIMIT 1;

-- name: ReserveHostingReleaseSlot :execrows
INSERT INTO hosting_release_slots(destination_id,host_port,binding_id) VALUES($1,$2,$3) ON CONFLICT DO NOTHING;

-- name: LockHostingReleaseSlot :one
SELECT binding_id FROM hosting_release_slots WHERE destination_id=$1 AND host_port=$2 FOR UPDATE;

-- name: HostingReleasePortReferenced :one
SELECT EXISTS(
 SELECT 1 FROM hosting_proxy_routes r JOIN hosting_snapshots hs ON hs.operation_id=r.selected_release_operation_id
 WHERE r.enabled AND (hs.body->>'destination_id')=sqlc.arg(destination_id)::text AND (hs.body->>'host_port')::integer=sqlc.arg(host_port)::integer
 UNION ALL
 SELECT 1 FROM hosting_proxy_release_references ref JOIN hosting_proxy_operations o ON o.id=ref.operation_id JOIN hosting_proxies p ON p.id=o.proxy_id JOIN hosting_snapshots hs ON hs.operation_id=ref.release_operation_id
 WHERE (hs.body->>'destination_id')=sqlc.arg(destination_id)::text AND (hs.body->>'host_port')::integer=sqlc.arg(host_port)::integer
 AND (p.loaded_generation=o.generation OR p.hold_operation_id=o.id OR o.state NOT IN ('applied','rolled_back','refused','superseded','canceled'))
);

-- name: HostingReleaseSlotReusable :one
SELECT NOT EXISTS(SELECT 1 FROM hosting_release_claims c WHERE c.destination_id=sqlc.arg(destination_id) AND c.host_port=sqlc.arg(host_port) AND c.state IN ('reserved','held'))
AND NOT EXISTS(SELECT 1 FROM hosting_retained_releases r WHERE r.destination_id=sqlc.arg(destination_id) AND r.host_port=sqlc.arg(host_port) AND r.operation_id<>sqlc.arg(previous_operation_id)::text AND (r.state='cleanup_pending' OR (r.state<>'removed' AND r.retained_running)))
AND NOT EXISTS(SELECT 1 FROM hosting_binding_state b WHERE b.destination_id=sqlc.arg(destination_id) AND b.host_port=sqlc.arg(host_port) AND b.binding_id<>sqlc.arg(binding_id))
AND NOT EXISTS(SELECT 1 FROM hosting_proxies p WHERE p.destination_id=sqlc.arg(destination_id) AND (p.configuration->>'management_port')::integer=sqlc.arg(host_port));

-- name: CreateHostingReleaseClaim :execrows
INSERT INTO hosting_release_claims(operation_id,destination_id,host_port,binding_id,state) VALUES($1,$2,$3,$4,'reserved');

-- name: GetHostingRetainedReleases :many
SELECT operation_id,body,state,retained_running FROM hosting_retained_releases WHERE binding_id=$1 AND state<>'removed' ORDER BY operation_id;

-- name: RecordHostingHealthyRelease :execrows
INSERT INTO hosting_retained_releases(operation_id,binding_id,destination_id,host_port,body,body_hash,state,retained_running)
VALUES($1,$2,$3,$4,sqlc.arg(body)::jsonb,'sha256:'||encode(sha256(convert_to((sqlc.arg(body)::jsonb)::text,'UTF8')),'hex'),'healthy',true);

-- name: RetainPreviousHostingRelease :execrows
UPDATE hosting_retained_releases SET state='retained',retained_running=$2 WHERE operation_id=$1 AND state='healthy';

-- name: RetainHostingReleaseClaim :execrows
UPDATE hosting_release_claims SET state='retained' WHERE operation_id=$1 AND state IN ('reserved','held');

-- name: HoldHostingReleaseClaim :execrows
UPDATE hosting_release_claims SET state='held' WHERE operation_id=$1 AND state='reserved';

-- name: ReleaseHostingFailedClaim :execrows
UPDATE hosting_release_claims SET state='released' WHERE operation_id=$1;

-- name: GetHostingRetainedRelease :one
SELECT operation_id,body,state,retained_running,coalesce(cleanup_operation_id,'') AS cleanup_operation_id FROM hosting_retained_releases WHERE operation_id=$1;

-- name: CreateHostingCleanupTarget :execrows
INSERT INTO hosting_cleanup_targets(operation_id,target_operation_id,plan,plan_hash) VALUES($1,$2,$3,$4);

-- name: CreateHostingCleanupTaskTarget :execrows
INSERT INTO hosting_task_targets(run_id,operation_id,binding_id,runner_id,mode) VALUES($1,$2,$3,$4,'cleanup');

-- name: MarkHostingCleanupPending :execrows
UPDATE hosting_retained_releases SET state='cleanup_pending',cleanup_operation_id=$2 WHERE operation_id=$1 AND state='retained';

-- name: MarkHostingCleanupRemoved :execrows
UPDATE hosting_retained_releases SET state='removed',retained_running=false WHERE operation_id=$1 AND state='cleanup_pending' AND cleanup_operation_id=$2;

-- name: ReleaseHostingCleanupClaim :execrows
UPDATE hosting_release_claims SET state='released' WHERE operation_id=$1;

-- name: ReleaseUnusedHostingSlot :execrows
DELETE FROM hosting_release_slots s WHERE s.destination_id=sqlc.arg(destination_id) AND s.host_port=sqlc.arg(host_port)
AND NOT EXISTS(SELECT 1 FROM hosting_release_claims c WHERE c.destination_id=s.destination_id AND c.host_port=s.host_port AND c.state<>'released')
AND NOT EXISTS(SELECT 1 FROM hosting_retained_releases r WHERE r.destination_id=s.destination_id AND r.host_port=s.host_port AND r.state<>'removed')
AND NOT EXISTS(SELECT 1 FROM hosting_binding_state b WHERE b.destination_id=s.destination_id AND b.host_port=s.host_port);

-- name: HostingReleaseReferenced :one
SELECT EXISTS(
 SELECT 1 FROM hosting_proxy_routes r WHERE r.enabled AND r.selected_release_operation_id=$1
 UNION ALL
 SELECT 1 FROM hosting_proxy_release_references ref JOIN hosting_proxy_operations o ON o.id=ref.operation_id JOIN hosting_proxies p ON p.id=o.proxy_id
 WHERE ref.release_operation_id=$1 AND (p.loaded_generation=o.generation OR p.hold_operation_id=o.id OR o.state NOT IN ('applied','rolled_back','refused','superseded','canceled'))
);
