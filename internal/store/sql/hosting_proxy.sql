-- name: ProxyPlacement :one
SELECT s.id AS server_id,s.runner_id,d.id AS destination_id FROM servers s JOIN destinations d ON d.server_id=s.id WHERE s.id=$1 AND d.runtime_kind='docker_compose';

-- name: ProxyRecoveryCompleteAuthority :one
SELECT authority FROM hosting_proxy_receipts WHERE operation_id=$1 AND phase='complete';

-- name: ProxyRecoveryLockLease :one
SELECT * FROM run_leases WHERE id=$1 AND run_id=$2 FOR UPDATE;

-- name: ProxyRecoveryTerminalTask :one
SELECT COALESCE((EXISTS(SELECT 1 FROM task_runs t JOIN hosting_proxy_task_targets x ON x.run_id=t.id
 WHERE t.id=sqlc.arg(run_id)::text AND t.status='failed' AND t.finished_at IS NOT NULL AND t.next_attempt=sqlc.arg(next_attempt)::integer
 AND t.runner_id=sqlc.arg(runner_id)::text AND x.operation_id=sqlc.arg(operation_id)::text AND x.proxy_id=sqlc.arg(proxy_id)::text
 AND x.server_id=sqlc.arg(server_id)::text AND x.destination_id=sqlc.arg(destination_id)::text AND x.runner_id=sqlc.arg(runner_id)::text AND x.mode='apply')
 AND NOT EXISTS(SELECT 1 FROM run_leases l JOIN hosting_proxy_task_targets x ON x.run_id=l.run_id WHERE x.proxy_id=sqlc.arg(proxy_id)::text AND l.status='active')),false)::boolean;

-- name: ProxyRecoveryAdmission :one
SELECT * FROM hosting_proxy_admissions WHERE operation_id=$1;

-- name: ProxyRecoveryLockHostnameFact :one
SELECT to_jsonb(g)::jsonb FROM hosting_proxy_hostname_grants g WHERE id=$1 FOR SHARE;

-- name: ProxyRecoveryLockDestinationFact :one
SELECT to_jsonb(g)::jsonb FROM destination_project_grants g WHERE destination_id=$1 AND project_id=$2 FOR SHARE;

-- name: ProxyRecoveryDeferredFacts :one
SELECT COALESCE(jsonb_agg(jsonb_build_object('id',id,'snapshot_hash',snapshot_hash) ORDER BY id),'[]'::jsonb)::jsonb FROM hosting_proxy_operations WHERE proxy_id=$1 AND id<>$2 AND state IN ('queued','dispatched','prepared','applying','uncertain');

-- name: ProxyRecoveryCompeting :one
SELECT EXISTS(SELECT 1 FROM hosting_proxy_operations o WHERE o.proxy_id=$1 AND o.id<>$2 AND o.state IN ('queued','dispatched','prepared','applying','uncertain') AND (o.state<>'queued' OR o.snapshot->'verified_switch' IS NOT NULL OR EXISTS(SELECT 1 FROM hosting_proxy_admissions a WHERE a.operation_id=o.id)));

-- name: ProxyRecoveryResolution :one
SELECT * FROM hosting_proxy_recovery_resolutions WHERE operation_id=$1;

-- name: ProxyRecoveryPlanByKey :one
SELECT * FROM hosting_proxy_recovery_plans WHERE operation_id=$1 AND request_key=$2;

-- name: ProxyRecoveryPlanPending :one
SELECT EXISTS(SELECT 1 FROM hosting_proxy_recovery_plans WHERE operation_id=$1 AND expires_at>clock_timestamp());

-- name: ProxyRecoveryCreatePlan :exec
INSERT INTO hosting_proxy_recovery_plans(id,operation_id,actor_id,request_key,request_hash,body,body_hash,created_at,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9);

-- name: ProxyRecoveryPlanForApply :one
SELECT body FROM hosting_proxy_recovery_plans WHERE id=$1 AND operation_id=$2 AND actor_id=$3 FOR SHARE;

-- name: ProxyRecoveryRestoreRoute :execrows
UPDATE hosting_proxy_routes SET selected_release_operation_id=$2,version=version+1,updated_at=clock_timestamp() WHERE id=$1 AND version=$3 AND selected_release_operation_id=$4;

-- name: ProxyRecoveryCreateResolution :exec
INSERT INTO hosting_proxy_recovery_resolutions(id,operation_id,plan_id,actor_id,plan_hash,request_key,request_hash,complete_receipt_id,complete_receipt_hash,convergence_operation_id,body,body_hash,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13);

-- name: ProxyGet :one
SELECT p.*,EXISTS(SELECT 1 FROM hosting_proxy_restore_holds h WHERE h.proxy_id=p.id) AS restore_held FROM hosting_proxies p WHERE p.id=$1;

-- name: ProxyGetByServer :one
SELECT p.*,EXISTS(SELECT 1 FROM hosting_proxy_restore_holds h WHERE h.proxy_id=p.id) AS restore_held FROM hosting_proxies p WHERE p.server_id=$1;

-- name: ProxyLock :one
SELECT * FROM hosting_proxies WHERE id=$1 FOR UPDATE;

-- name: ProxyCreate :execrows
INSERT INTO hosting_proxies(id,server_id,destination_id,runner_id,control_project_id,configuration,created_by) VALUES($1,$2,$3,$4,$5,$6,$7);

-- name: ProxySetEnabled :execrows
UPDATE hosting_proxies SET enabled=$2,updated_at=clock_timestamp() WHERE id=$1;

-- name: ProxyNextGeneration :one
UPDATE hosting_proxies SET desired_generation=desired_generation+1,updated_at=clock_timestamp() WHERE id=$1 RETURNING desired_generation;

-- name: ProxyRunnerEligible :one
SELECT EXISTS(SELECT 1 FROM runners WHERE id=$1 AND status NOT IN ('disabled','revoked') AND 'proxy_apply'=ANY(capabilities));

-- name: ProxyManagementPortAvailable :one
SELECT NOT EXISTS(SELECT 1 FROM hosting_binding_state b WHERE b.destination_id=sqlc.arg(destination_id) AND b.host_port=sqlc.arg(host_port))
AND NOT EXISTS(SELECT 1 FROM hosting_release_slots s WHERE s.destination_id=sqlc.arg(destination_id) AND s.host_port=sqlc.arg(host_port));

-- name: ProxyLockUser :one
SELECT id,global_role FROM users WHERE id=$1 AND status='active' AND archived_at IS NULL FOR SHARE;

-- name: ProxyLockMember :one
SELECT role FROM project_members WHERE project_id=$1 AND user_id=$2 FOR SHARE;

-- name: ProxyLockServiceAccount :one
SELECT sa.project_id,sa.role FROM service_accounts sa JOIN users u ON u.id=sa.owner_user_id WHERE sa.id=$1 AND sa.status='active' AND u.status='active' FOR SHARE OF sa,u;

-- name: ProxyLockLegacyToken :one
SELECT roles FROM api_tokens WHERE id=$1 AND service_account_id IS NULL
AND status='active' AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at>clock_timestamp()) FOR SHARE;

-- name: ProxyLegacyTokenValid :one
SELECT COALESCE(bool_and(status='active' AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at>clock_timestamp())),true)::boolean
FROM api_tokens WHERE id=$1 AND service_account_id IS NULL;

-- name: ProxyLockProject :one
SELECT id FROM projects WHERE id=$1 AND archived_at IS NULL FOR SHARE;

-- name: ProxyLockProjectLifecycle :one
SELECT id FROM projects WHERE id=$1 AND archived_at IS NULL FOR UPDATE;

-- name: ProxyControlProjectUsed :one
SELECT EXISTS(SELECT 1 FROM hosting_proxies WHERE control_project_id=$1);

-- name: ProxyListGrants :many
SELECT * FROM hosting_proxy_hostname_grants WHERE proxy_id=$1 ORDER BY id;

-- name: ProxyGetGrant :one
SELECT * FROM hosting_proxy_hostname_grants WHERE id=$1;

-- name: ProxyCreateGrant :one
INSERT INTO hosting_proxy_hostname_grants(id,proxy_id,project_id,hostname,wildcard,granted_by) VALUES($1,$2,$3,$4,$5,$6)
ON CONFLICT(proxy_id,project_id,hostname,wildcard) DO UPDATE SET revoked_at=NULL,granted_by=excluded.granted_by RETURNING *;

-- name: ProxyRevokeGrant :execrows
UPDATE hosting_proxy_hostname_grants SET revoked_at=clock_timestamp() WHERE id=$1 AND proxy_id=$2 AND revoked_at IS NULL;

-- name: ProxyWithdrawGrantRoutes :execrows
UPDATE hosting_proxy_routes SET enabled=false,version=version+1,updated_at=clock_timestamp() WHERE grant_id=$1 AND enabled;

-- name: ProxyWithdrawAllRoutes :execrows
UPDATE hosting_proxy_routes SET enabled=false,version=version+1,updated_at=clock_timestamp() WHERE proxy_id=$1 AND enabled;

-- name: ProxyGetRoute :one
SELECT * FROM hosting_proxy_routes WHERE id=$1;

-- name: ProxyListResourceRoutes :many
SELECT * FROM hosting_proxy_routes WHERE resource_id=$1 ORDER BY id;

-- name: ProxyListRoutes :many
SELECT * FROM hosting_proxy_routes WHERE proxy_id=$1 ORDER BY resource_id,id;

-- name: ProxyCreateRoute :execrows
INSERT INTO hosting_proxy_routes(id,proxy_id,project_id,resource_id,binding_id,grant_id,hostname,path_prefix,version,selected_release_operation_id,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,1,$9,$10);

-- name: ProxyUpdateRoute :execrows
UPDATE hosting_proxy_routes SET grant_id=$2,hostname=$3,path_prefix=$4,version=version+1,selected_release_operation_id=$5,enabled=true,updated_at=clock_timestamp() WHERE id=$1 AND version=$6;

-- name: ProxyDeleteRoute :execrows
UPDATE hosting_proxy_routes SET enabled=false,version=version+1,updated_at=clock_timestamp() WHERE id=$1 AND version=$2;

-- name: ProxyResourceTarget :one
SELECT r.project_id,r.id AS resource_id,b.id AS binding_id,b.destination_id,s.runner_id,d.server_id FROM resources r JOIN resource_bindings b ON b.resource_id=r.id JOIN destinations d ON d.id=b.destination_id JOIN servers s ON s.id=d.server_id WHERE r.id=$1;

-- name: ProxyLockResource :one
SELECT id FROM resources WHERE id=$1 FOR UPDATE;

-- name: ProxyReleaseTarget :one
SELECT * FROM hosting_retained_releases WHERE operation_id=$1;

-- name: ProxyIdempotency :one
SELECT id,request_hash FROM hosting_proxy_operations WHERE proxy_id=$1 AND idempotency_key=$2;

-- name: ProxyCreateOperation :execrows
INSERT INTO hosting_proxy_operations(id,proxy_id,project_id,route_id,generation,idempotency_key,request_hash,state,snapshot,snapshot_hash,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,'queued',$8,$9,$10);

-- name: ProxyReferenceRelease :execrows
INSERT INTO hosting_proxy_release_references(operation_id,route_id,release_operation_id) VALUES($1,$2,$3);

-- name: ProxySupersedeUnadmitted :execrows
UPDATE hosting_proxy_operations o SET state='superseded',updated_at=clock_timestamp() WHERE o.proxy_id=$1 AND o.state IN ('queued','dispatched','prepared') AND NOT EXISTS(SELECT 1 FROM hosting_proxy_admissions a WHERE a.operation_id=o.id);

-- name: ProxyClearUnadmittedActive :execrows
UPDATE hosting_proxies p SET active_operation_id=NULL WHERE p.id=$1 AND p.hold_operation_id IS NULL AND NOT EXISTS(SELECT 1 FROM hosting_proxy_admissions a WHERE a.operation_id=p.active_operation_id);

-- name: ProxyOperation :one
SELECT o.*,EXISTS(SELECT 1 FROM hosting_proxies p WHERE p.hold_operation_id=o.id) AS mutation_hold,
EXISTS(SELECT 1 FROM hosting_proxy_restore_holds h WHERE h.proxy_id=o.proxy_id) AS restore_held,
(SELECT jsonb_build_object('id',receipt_id,'body_hash',body_hash,'created_at',created_at) FROM hosting_proxy_receipts WHERE operation_id=o.id AND phase='prepared') AS prepared_receipt,
(SELECT body FROM hosting_proxy_receipts WHERE operation_id=o.id AND phase='prepared') AS prepared,
(SELECT jsonb_build_object('id',receipt_id,'body_hash',body_hash,'created_at',created_at) FROM hosting_proxy_receipts WHERE operation_id=o.id AND phase='complete') AS complete_receipt,
(SELECT body FROM hosting_proxy_receipts WHERE operation_id=o.id AND phase='complete') AS result
 ,COALESCE((EXISTS(SELECT 1 FROM audit_events WHERE target_id=o.id AND action='hosting.route_switch_metadata_conflict') AND NOT EXISTS(SELECT 1 FROM hosting_proxy_recovery_resolutions WHERE operation_id=o.id)),false)::boolean AS metadata_conflict
FROM hosting_proxy_operations o WHERE o.id=$1;

-- name: ProxyRouteOperation :one
SELECT o.id FROM hosting_proxy_operations o WHERE o.route_id=$1 OR EXISTS(SELECT 1 FROM hosting_proxy_release_references ref WHERE ref.operation_id=o.id AND ref.route_id=$1) ORDER BY CASE WHEN EXISTS(SELECT 1 FROM hosting_proxies p WHERE p.hold_operation_id=o.id) THEN 0 ELSE 1 END,o.generation DESC LIMIT 1;

-- name: ProxyQueuedRecovery :many
SELECT id FROM hosting_proxy_operations WHERE state='queued' AND run_id IS NULL ORDER BY updated_at,id LIMIT 100 FOR UPDATE SKIP LOCKED;

-- name: ProxyTouchOperation :execrows
UPDATE hosting_proxy_operations SET updated_at=clock_timestamp() WHERE id=$1;

-- name: ProxyDispatch :execrows
UPDATE hosting_proxy_operations SET plan=$2,plan_hash=$3,run_id=$4,state='dispatched',updated_at=clock_timestamp() WHERE id=$1 AND state='queued';

-- name: ProxySetActive :execrows
UPDATE hosting_proxies SET active_operation_id=$2 WHERE id=$1 AND active_operation_id IS NULL AND hold_operation_id IS NULL;

-- name: ProxyCreateTaskTarget :execrows
INSERT INTO hosting_proxy_task_targets(run_id,operation_id,proxy_id,server_id,destination_id,runner_id,mode) VALUES($1,$2,$3,$4,$5,$6,'apply');

-- name: ProxyIsTask :one
SELECT EXISTS(SELECT 1 FROM hosting_proxy_task_targets WHERE run_id=$1);

-- name: ProxyClaimEligible :one
SELECT EXISTS(SELECT 1 FROM hosting_proxy_task_targets t JOIN hosting_proxy_operations o ON o.id=t.operation_id JOIN hosting_proxies p ON p.id=t.proxy_id JOIN runners r ON r.id=t.runner_id
WHERE t.run_id=$1 AND t.runner_id=$2 AND t.mode='apply' AND o.state IN ('dispatched','prepared') AND o.run_id=t.run_id AND p.active_operation_id=o.id AND p.hold_operation_id IS NULL AND r.status NOT IN ('disabled','revoked') AND 'proxy_apply'=ANY(r.capabilities)
AND (NOT (o.snapshot ? 'verified_switch') OR 'hosting_proxy_verified_switch_v1'=ANY(r.capabilities))
AND NOT EXISTS(SELECT 1 FROM hosting_proxy_restore_holds h WHERE h.proxy_id=p.id));

-- name: ProxyCheckAuthority :one
SELECT l.id FROM run_leases l JOIN task_runs r ON r.id=l.run_id JOIN hosting_proxy_task_targets t ON t.run_id=r.id JOIN hosting_proxy_operations o ON o.id=t.operation_id JOIN hosting_proxies p ON p.id=t.proxy_id JOIN runners agent ON agent.id=t.runner_id
WHERE l.id=$1 AND l.run_id=$2 AND l.runner_id=$3 AND l.attempt=$4 AND l.fence=$5 AND o.id=$6 AND t.mode='apply' AND t.runner_id=l.runner_id AND t.server_id=p.server_id AND t.destination_id=p.destination_id AND t.runner_id=p.runner_id AND r.run_spec->>'type'='proxy_apply' AND r.run_spec->>'proxy_operation_id'=o.id AND r.run_spec->>'proxy_mode'='apply'
AND (NOT sqlc.arg(live)::boolean OR (l.status='active' AND l.expires_at>clock_timestamp() AND o.state NOT IN ('cancel_requested','canceled','superseded') AND r.status NOT IN ('succeeded','failed','canceled') AND agent.status NOT IN ('disabled','revoked') AND 'proxy_apply'=ANY(agent.capabilities) AND (NOT (o.snapshot ? 'verified_switch') OR 'hosting_proxy_verified_switch_v1'=ANY(agent.capabilities)))) FOR UPDATE OF l;

-- name: ProxyReceipt :one
SELECT * FROM hosting_proxy_receipts WHERE operation_id=$1 AND phase=$2;

-- name: ProxyInsertReceipt :execrows
INSERT INTO hosting_proxy_receipts(operation_id,phase,receipt_id,authority,body,body_hash) VALUES($1,$2,$3,$4,$5,$6);

-- name: ProxyPinEngine :execrows
UPDATE hosting_proxies SET daemon_id=$2,static_hash=$3 WHERE id=$1 AND (daemon_id IS NULL OR daemon_id=$2) AND (static_hash IS NULL OR static_hash=$3);

-- name: ProxySetState :execrows
UPDATE hosting_proxy_operations SET state=$2,updated_at=clock_timestamp() WHERE id=$1;

-- name: ProxyAdmit :execrows
INSERT INTO hosting_proxy_admissions(operation_id,run_id,lease_id,authority,plan_hash) VALUES($1,$2,$3,$4,$5);

-- name: ProxyHold :execrows
UPDATE hosting_proxies SET hold_operation_id=$2 WHERE id=$1 AND active_operation_id=$2 AND hold_operation_id IS NULL;

-- name: ProxySettle :execrows
UPDATE hosting_proxies SET loaded_generation=$3,hold_operation_id=NULL,active_operation_id=NULL,updated_at=clock_timestamp() WHERE id=$1 AND active_operation_id=$2 AND hold_operation_id=$2;

-- name: ProxySettleUnadmitted :execrows
UPDATE hosting_proxies SET active_operation_id=NULL,updated_at=clock_timestamp() WHERE id=$1 AND active_operation_id=$2 AND hold_operation_id IS NULL;

-- name: ProxyVerifiedSwitchRunnerEligible :one
SELECT EXISTS(SELECT 1 FROM runners WHERE id=$1 AND status NOT IN ('disabled','revoked') AND 'proxy_apply'=ANY(capabilities) AND 'hosting_proxy_verified_switch_v1'=ANY(capabilities));

-- name: ProxyLoadedAppliedOperation :one
SELECT o.id FROM hosting_proxy_operations o WHERE o.proxy_id=$1 AND o.generation=$2 AND o.state='applied'
AND EXISTS(SELECT 1 FROM hosting_proxy_receipts r WHERE r.operation_id=o.id AND r.phase='complete' AND r.body->>'outcome'='applied' AND (r.body->>'loaded_generation')::bigint=o.generation);

-- name: ProxyStrictPending :many
SELECT o.id FROM hosting_proxy_operations o JOIN hosting_proxies p ON p.id=o.proxy_id WHERE o.proxy_id=$1 AND o.snapshot ? 'verified_switch'
AND (p.hold_operation_id=o.id OR o.state NOT IN ('applied','rolled_back','refused','superseded','canceled')) ORDER BY o.generation;

-- name: ProxyPending :one
SELECT EXISTS(SELECT 1 FROM hosting_proxy_operations o JOIN hosting_proxies p ON p.id=o.proxy_id WHERE o.proxy_id=$1 AND (p.hold_operation_id=o.id OR o.state NOT IN ('applied','rolled_back','refused','superseded','canceled')));

-- name: ProxySwitchRoute :execrows
UPDATE hosting_proxy_routes SET selected_release_operation_id=$2,version=version+1,updated_at=clock_timestamp()
WHERE id=$1 AND version=$3 AND selected_release_operation_id=$4 AND enabled;

-- name: ProxyRestoreSwitchRoute :execrows
UPDATE hosting_proxy_routes SET selected_release_operation_id=$2,version=version+1,updated_at=clock_timestamp()
WHERE id=$1 AND version=$3 AND selected_release_operation_id=$4;

-- name: ProxyReferencePriorRelease :execrows
INSERT INTO hosting_proxy_switch_prior_references(operation_id,route_id,prior_proxy_operation_id,prior_release_operation_id,prior_version) VALUES($1,$2,$3,$4,$5);

-- name: ProxyPriorReleaseReferences :many
SELECT * FROM hosting_proxy_switch_prior_references WHERE operation_id=$1 ORDER BY route_id;

-- name: ProxyDeferredWithdrawals :many
SELECT id FROM hosting_proxy_operations WHERE proxy_id=$1 AND generation>$2 AND state='queued' AND NOT (snapshot ? 'verified_switch') ORDER BY generation;

-- name: ProxyLockVerifiedSwitchRunner :one
SELECT id FROM runners WHERE id=$1 AND status NOT IN ('disabled','revoked') AND 'proxy_apply'=ANY(capabilities) AND 'hosting_proxy_verified_switch_v1'=ANY(capabilities) FOR SHARE;

-- name: ProxySwitchReadProject :one
SELECT r.project_id FROM resources r JOIN projects p ON p.id=r.project_id WHERE r.id=$1 AND p.archived_at IS NULL;

-- name: ProxySwitchReadAllowed :one
SELECT (CASE
 WHEN EXISTS(SELECT 1 FROM users scope_user WHERE scope_user.id=$1 AND scope_user.status='active' AND scope_user.archived_at IS NULL) THEN
 EXISTS(SELECT 1 FROM users u WHERE u.id=$1 AND (u.global_role='system_admin' OR EXISTS(SELECT 1 FROM project_members m WHERE m.project_id=$2 AND m.user_id=u.id AND m.role IN ('owner','maintainer','viewer'))))
 WHEN EXISTS(SELECT 1 FROM api_tokens scope_token WHERE scope_token.id=$1 AND scope_token.service_account_id IS NULL AND scope_token.status='active' AND scope_token.revoked_at IS NULL AND (scope_token.expires_at IS NULL OR scope_token.expires_at>clock_timestamp())) THEN
 EXISTS(SELECT 1 FROM api_tokens role_token WHERE role_token.id=$1 AND 'system_admin'=ANY(role_token.roles))
 ELSE EXISTS(SELECT 1 FROM service_accounts sa JOIN users u ON u.id=sa.owner_user_id WHERE sa.id=$1 AND sa.status='active' AND u.status='active' AND sa.project_id=$2 AND sa.role IN ('owner','maintainer','viewer')) END)::boolean;

-- name: ProxySwitchReadBindingState :one
SELECT restore_admitted,hold_operation_id FROM hosting_binding_state WHERE binding_id=$1;

-- name: ProxySwitchReadEnabledRoutes :many
SELECT id FROM hosting_proxy_routes WHERE proxy_id=$1 AND enabled ORDER BY id LIMIT 33;

-- name: ProxySwitchReadPendingOperation :one
SELECT o.id FROM hosting_proxy_operations o WHERE o.proxy_id=sqlc.arg(proxy_id)::text AND o.project_id=sqlc.arg(project_id)::text AND o.snapshot->'verified_switch'->'routes'->0->'prior_target'->>'resource_id'=sqlc.arg(resource_id)::text AND (o.id=sqlc.arg(active_operation_id)::text OR o.id=sqlc.arg(hold_operation_id)::text OR o.state NOT IN ('applied','rolled_back','refused','superseded','canceled')) ORDER BY o.generation LIMIT 1;

-- name: ProxySwitchReadTargets :many
SELECT rel.operation_id,rel.state,rel.retained_running,rel.body
 FROM hosting_retained_releases rel JOIN hosting_operations o ON o.id=rel.operation_id
 WHERE o.project_id=sqlc.arg(project_id)::text AND o.resource_id=sqlc.arg(resource_id)::text AND o.binding_id=sqlc.arg(binding_id)::text AND rel.binding_id=sqlc.arg(binding_id)::text
 AND rel.destination_id=sqlc.arg(destination_id)::text AND rel.state IN ('healthy','retained') AND rel.retained_running
 AND rel.body->'snapshot'->>'resource_id'=sqlc.arg(resource_id)::text AND rel.body->'snapshot'->>'binding_id'=sqlc.arg(binding_id)::text
 AND rel.body->'snapshot'->>'destination_id'=sqlc.arg(destination_id)::text AND rel.body->'snapshot'->>'server_id'=sqlc.arg(server_id)::text
 AND rel.body->'snapshot'->>'runner_id'=sqlc.arg(runner_id)::text AND rel.body->'release'->>'daemon_id'=sqlc.arg(daemon_id)::text
 AND rel.operation_id>sqlc.arg(after_operation_id)::text ORDER BY rel.operation_id LIMIT 51;
