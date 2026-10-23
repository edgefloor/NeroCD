-- name: StorageContinuitySchemaPresent :one
SELECT (to_regclass(current_schema()||'.hosting_storage_route_continuities') IS NOT NULL)::boolean AS present;

-- name: StorageContinuitySchemaCompatible :one
SELECT nerocd_hosting_storage_route_continuity_schema_compatible()::boolean;

-- name: StorageContinuityExclusive :one
SELECT EXISTS(SELECT 1 FROM hosting_storage_route_continuities WHERE proxy_id=$1 AND state IN ('reserved','unresolved'));

-- name: StorageContinuityProxyPending :one
SELECT EXISTS(SELECT 1 FROM hosting_storage_route_continuities WHERE proxy_id=$1 AND state IN ('proxy_pending','proxy_failed'));

-- name: StorageContinuityRouteState :one
SELECT coalesce((SELECT state FROM hosting_storage_route_continuities WHERE proxy_id=$1 AND descriptor->>'resource_id'=sqlc.arg(resource_id)::text AND state IN ('reserved','unresolved','proxy_pending','proxy_failed') ORDER BY created_at DESC LIMIT 1),'')::text AS state;

-- name: StorageContinuityForeignPortReferenced :one
SELECT EXISTS(
 SELECT 1 FROM hosting_proxy_routes route JOIN hosting_snapshots s ON s.operation_id=route.selected_release_operation_id WHERE route.enabled AND s.body->>'destination_id'=sqlc.arg(destination_id)::text AND (s.body->>'host_port')::integer=sqlc.arg(host_port)::integer AND NOT(route.proxy_id=sqlc.arg(proxy_id) AND route.resource_id=sqlc.arg(resource_id) AND route.binding_id=sqlc.arg(binding_id) AND route.selected_release_operation_id=sqlc.arg(previous_operation_id))
 UNION ALL SELECT 1 FROM hosting_proxy_release_references ref JOIN hosting_proxy_operations o ON o.id=ref.operation_id JOIN hosting_proxies p ON p.id=o.proxy_id JOIN hosting_snapshots s ON s.operation_id=ref.release_operation_id WHERE s.body->>'destination_id'=sqlc.arg(destination_id)::text AND (s.body->>'host_port')::integer=sqlc.arg(host_port)::integer AND (p.loaded_generation=o.generation OR p.hold_operation_id=o.id OR o.state NOT IN ('applied','rolled_back','refused','superseded','canceled')) AND NOT(o.id=sqlc.arg(loaded_proxy_operation_id) AND ref.release_operation_id=sqlc.arg(previous_operation_id) AND ref.route_id=ANY(sqlc.arg(route_ids)::text[]))
 UNION ALL SELECT 1 FROM hosting_proxy_switch_prior_references prior_ref JOIN hosting_proxy_operations o ON o.id=prior_ref.operation_id JOIN hosting_proxies p ON p.id=o.proxy_id JOIN hosting_snapshots s ON s.operation_id=prior_ref.prior_release_operation_id WHERE s.body->>'destination_id'=sqlc.arg(destination_id)::text AND (s.body->>'host_port')::integer=sqlc.arg(host_port)::integer AND (p.hold_operation_id=o.id OR o.state NOT IN ('applied','rolled_back','refused','superseded','canceled'))
);

-- name: ReadStorageRouteContinuity :one
SELECT c.descriptor,c.descriptor_hash,c.state,c.accepted_request,o.request_hash FROM hosting_storage_route_continuities c JOIN hosting_operations o ON o.id=c.operation_id WHERE c.operation_id=$1;

-- name: StorageContinuityAutomatic :one
SELECT EXISTS(SELECT 1 FROM hosting_auto_deploy_origins WHERE operation_id=$1 OR deployment_operation_id=$1);

-- name: SetStorageContinuityReservedState :execrows
UPDATE hosting_storage_route_continuities SET state=$2 WHERE operation_id=$1 AND state='reserved';

-- name: QueueStorageContinuity :execrows
UPDATE hosting_storage_route_continuities SET state='proxy_pending',successor_proxy_operation_id=$2 WHERE operation_id=$1 AND state='reserved';

-- name: SettleStorageContinuityProxy :execrows
UPDATE hosting_storage_route_continuities SET state=$2 WHERE successor_proxy_operation_id=$1 AND state='proxy_pending';

-- name: ReleaseUnmutatedStorageContinuity :execrows
UPDATE hosting_storage_route_continuities SET state='released' WHERE hosting_storage_route_continuities.operation_id=$1 AND state='reserved' AND NOT EXISTS(SELECT 1 FROM hosting_storage_operation_admissions WHERE operation_id=$1) AND NOT EXISTS(SELECT 1 FROM hosting_apply_admissions WHERE operation_id=$1);

-- name: CreateStorageRouteContinuity :exec
INSERT INTO hosting_storage_route_continuities(operation_id,proxy_id,descriptor,descriptor_hash,accepted_request) VALUES($1,$2,$3,$4,$5);
