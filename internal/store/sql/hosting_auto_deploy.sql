-- name: AutoReadControl :one
SELECT * FROM hosting_auto_deploy_controls WHERE resource_id=$1;
-- name: AutoLockControl :one
SELECT * FROM hosting_auto_deploy_controls WHERE resource_id=$1 FOR UPDATE;
-- name: AutoPutControl :one
INSERT INTO hosting_auto_deploy_controls(resource_id,policy_id,policy_generation,body,body_hash) VALUES($1,$2,$3,$4,$5)
ON CONFLICT(resource_id) DO UPDATE SET policy_id=EXCLUDED.policy_id,policy_generation=EXCLUDED.policy_generation,body=EXCLUDED.body,body_hash=EXCLUDED.body_hash RETURNING *;
-- name: AutoBumpManualEpoch :execrows
UPDATE hosting_auto_deploy_controls SET manual_epoch=manual_epoch+1 WHERE resource_id=$1;
-- name: AutoUpdateBaseline :execrows
UPDATE hosting_auto_deploy_controls SET body=$3,body_hash=$4 WHERE resource_id=$1 AND manual_epoch=$2;
-- name: AutoReadDemand :one
SELECT * FROM hosting_auto_deploy_demands WHERE resource_id=$1;
-- name: AutoLockDemand :one
SELECT * FROM hosting_auto_deploy_demands WHERE resource_id=$1 FOR UPDATE;
-- name: AutoReplaceDemand :execrows
INSERT INTO hosting_auto_deploy_demands(resource_id,id,policy_id,policy_generation,build_id,commit_sha,body,stage,reason,attempts,created_at,expires_at,next_retry_at,updated_at)
VALUES($1,$2,$3,$4,$5,$6,$7,'waiting_build','build_queued',$8,$9,$10,clock_timestamp(),clock_timestamp())
ON CONFLICT(resource_id) DO UPDATE SET id=EXCLUDED.id,policy_id=EXCLUDED.policy_id,policy_generation=EXCLUDED.policy_generation,build_id=EXCLUDED.build_id,commit_sha=EXCLUDED.commit_sha,body=EXCLUDED.body,stage=EXCLUDED.stage,reason=EXCLUDED.reason,attempts=EXCLUDED.attempts,created_at=EXCLUDED.created_at,expires_at=EXCLUDED.expires_at,next_retry_at=EXCLUDED.next_retry_at,updated_at=EXCLUDED.updated_at,last_wakeup_at='epoch',observed_loaded_generation=0
WHERE hosting_auto_deploy_demands.id<>EXCLUDED.id;
-- name: AutoAdvanceDemand :execrows
UPDATE hosting_auto_deploy_demands SET stage=sqlc.arg(stage),reason=sqlc.arg(reason),attempts=sqlc.arg(attempts),next_retry_at=clock_timestamp()+make_interval(secs=>sqlc.arg(retry_seconds)::integer),updated_at=clock_timestamp(),observed_loaded_generation=sqlc.arg(observed_loaded_generation) WHERE resource_id=sqlc.arg(resource_id) AND id=sqlc.arg(id);
-- name: AutoReadOrigin :one
SELECT * FROM hosting_auto_deploy_origins WHERE operation_id=$1;
-- name: AutoInsertOrigin :execrows
INSERT INTO hosting_auto_deploy_origins(operation_id,kind,resource_id,demand_id,build_id,deployment_operation_id,proxy_operation_id,body,body_hash) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9);
-- name: AutoRecoverDue :many
SELECT resource_id FROM hosting_auto_deploy_demands WHERE (stage IN ('waiting_build','waiting_deployment','waiting_switch') OR EXISTS(SELECT 1 FROM hosting_auto_deploy_origins o LEFT JOIN hosting_operations d ON d.id=o.deployment_operation_id LEFT JOIN hosting_proxy_operations p ON p.id=o.proxy_operation_id WHERE o.resource_id=hosting_auto_deploy_demands.resource_id AND ((o.kind='deployment' AND d.state NOT IN ('succeeded','failed','canceled')) OR (o.kind='switch' AND p.state NOT IN ('applied','rolled_back','refused','superseded','canceled'))))) AND next_retry_at<=clock_timestamp() AND last_wakeup_at<clock_timestamp()-interval '5 seconds' ORDER BY last_wakeup_at,resource_id LIMIT 16 FOR UPDATE SKIP LOCKED;
-- name: AutoTouchWakeup :exec
UPDATE hosting_auto_deploy_demands SET last_wakeup_at=clock_timestamp() WHERE resource_id=$1;
-- name: AutoUnsettledOrigins :many
SELECT o.* FROM hosting_auto_deploy_origins o LEFT JOIN hosting_operations d ON d.id=o.deployment_operation_id LEFT JOIN hosting_proxy_operations p ON p.id=o.proxy_operation_id
WHERE o.resource_id=$1 AND ((o.kind='deployment' AND d.state NOT IN ('succeeded','failed','canceled')) OR (o.kind='switch' AND p.state NOT IN ('applied','rolled_back','refused','superseded','canceled'))) ORDER BY o.created_at,o.operation_id LIMIT 4;
-- name: AutoOriginAdmitted :one
SELECT EXISTS(SELECT 1 FROM hosting_apply_admissions a WHERE a.operation_id=sqlc.arg(id)) OR EXISTS(SELECT 1 FROM hosting_proxy_admissions p WHERE p.operation_id=sqlc.arg(id));
-- name: AutoAppliedOrigins :many
SELECT o.* FROM hosting_auto_deploy_origins o JOIN hosting_proxy_operations p ON p.id=o.proxy_operation_id WHERE o.resource_id=$1 AND o.kind='switch' AND p.state='applied' ORDER BY p.generation DESC LIMIT 4;
-- name: AutoReadOptions :one
SELECT jsonb_build_object(
 'now',clock_timestamp(),'resource_id',r.id,'project_id',r.project_id,'resource_generation',r.generation,
 'source_generation',coalesce((r.source->'git'->>'generation')::bigint,0),
 'supported',r.kind='application' AND r.source->>'kind'='git','bound',b.id IS NOT NULL,
 'binding_id',b.id,'destination_id',d.id,'server_id',s.id,'runner_id',agent.id,
 'runtime_generation',coalesce(runtime.generation,0),'manual_epoch',coalesce(control.manual_epoch,0),
 'restore_hold',coalesce(NOT bs.restore_admitted,false) OR EXISTS(SELECT 1 FROM hosting_binding_restore_holds h WHERE h.binding_id=b.id) OR EXISTS(SELECT 1 FROM hosting_proxy_restore_holds h WHERE h.proxy_id=p.id),
 'destination_current',coalesce(d.runtime_kind='docker_compose' AND grant_destination.revoked_at IS NULL AND grant_destination.destination_id IS NOT NULL,false),
 'runner_eligible',coalesce(agent.status='active' AND agent.token_hash<>'' AND NOT('shell'=ANY(agent.capabilities))
 AND ARRAY['hosting_build_v1','hosting_build_trigger_revision_v1','local_build_deploy_v1','image_release_ports_v1','proxy_apply','hosting_proxy_verified_switch_v1']::text[]<@agent.capabilities
 AND (coalesce(runtime.generation,0)=0 OR 'hosting_runtime_env_v1'=ANY(agent.capabilities))
 AND (coalesce(source_version.body->>'credential_kind','public')<>'https_basic' OR 'hosting_git_https_basic_v1'=ANY(agent.capabilities))
 AND EXISTS(SELECT 1 FROM runner_enrollments e WHERE e.runner_id=agent.id AND e.used_at IS NOT NULL AND e.revoked_at IS NULL AND e.credential_hash=agent.token_hash),false),
 'healthy',bs.healthy,'healthy_snapshot',healthy_snapshot.body,
 'manual_healthy',coalesce(healthy_operation.state='succeeded' AND healthy_snapshot.body=bs.healthy->'snapshot' AND bs.hold_operation_id IS NULL AND NOT EXISTS(SELECT 1 FROM hosting_auto_deploy_origins o WHERE o.operation_id=healthy_operation.id),false),
 'proxy',to_jsonb(p),
 'loaded',jsonb_build_object('id',loaded.id,'state',loaded.state,'proxy_id',p.id,'generation',loaded.generation,'snapshot',loaded.snapshot,'result',receipt.body,'mutation_hold',p.hold_operation_id=loaded.id,'restore_ownership_held',EXISTS(SELECT 1 FROM hosting_proxy_restore_holds h WHERE h.proxy_id=p.id)),
 'routes',coalesce((SELECT jsonb_agg(route_doc ORDER BY route_id) FROM (
 SELECT route.id AS route_id,jsonb_build_object('row',to_jsonb(route),'grant',to_jsonb(hostname_grant),'release',release.body,'release_state',release.state,'retained_running',release.retained_running) AS route_doc
 FROM hosting_proxy_routes route LEFT JOIN hosting_proxy_hostname_grants hostname_grant ON hostname_grant.id=route.grant_id LEFT JOIN hosting_retained_releases release ON release.operation_id=route.selected_release_operation_id
 WHERE route.proxy_id=p.id AND route.resource_id=r.id AND route.enabled ORDER BY route.id LIMIT 33) eligible),'[]'::jsonb)
)::jsonb AS snapshot
FROM resources r
LEFT JOIN resource_bindings b ON b.resource_id=r.id
LEFT JOIN destinations d ON d.id=b.destination_id
LEFT JOIN servers s ON s.id=d.server_id
LEFT JOIN runners agent ON agent.id=s.runner_id
LEFT JOIN destination_project_grants grant_destination ON grant_destination.destination_id=d.id AND grant_destination.project_id=r.project_id
LEFT JOIN hosting_binding_state bs ON bs.binding_id=b.id
LEFT JOIN hosting_runtime_configurations runtime ON runtime.resource_id=r.id
LEFT JOIN hosting_auto_deploy_controls control ON control.resource_id=r.id
LEFT JOIN hosting_build_sources source ON source.resource_id=r.id
LEFT JOIN hosting_build_source_versions source_version ON source_version.source_id=source.id AND source_version.generation=source.generation
LEFT JOIN hosting_operations healthy_operation ON healthy_operation.id=bs.healthy->>'operation_id'
LEFT JOIN hosting_snapshots healthy_snapshot ON healthy_snapshot.operation_id=healthy_operation.id
LEFT JOIN hosting_proxies p ON p.server_id=s.id AND p.destination_id=d.id AND p.runner_id=s.runner_id
LEFT JOIN hosting_proxy_operations loaded ON loaded.proxy_id=p.id AND loaded.generation=p.loaded_generation
LEFT JOIN hosting_proxy_receipts receipt ON receipt.operation_id=loaded.id AND receipt.phase='complete'
WHERE r.id=$1;
