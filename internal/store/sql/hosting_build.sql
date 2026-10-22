-- name: HostingBuildPrincipalKind :one
SELECT kind FROM principals WHERE id=$1;
-- name: LockHostingBuildProject :one
SELECT id FROM projects WHERE id=$1 AND archived_at IS NULL FOR SHARE;
-- name: LockHostingBuildUser :one
SELECT status,global_role FROM users WHERE id=$1 AND archived_at IS NULL FOR SHARE;
-- name: LockHostingBuildMember :one
SELECT role FROM project_members WHERE project_id=$1 AND user_id=$2 FOR SHARE;
-- name: LockHostingBuildToken :one
SELECT roles FROM api_tokens WHERE id=$1 AND service_account_id IS NULL AND status='active' AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at>clock_timestamp()) FOR SHARE;
-- name: LockHostingBuildServiceAccount :one
SELECT sa.project_id,sa.role FROM service_accounts sa JOIN users u ON u.id=sa.owner_user_id WHERE sa.id=$1 AND sa.status='active' AND u.status='active' AND u.archived_at IS NULL FOR SHARE OF sa,u;
-- name: LockHostingBuildResource :one
SELECT project_id,generation FROM resources WHERE id=$1 FOR UPDATE;
-- name: LockHostingBuildSource :one
SELECT v.body FROM hosting_build_sources h JOIN hosting_build_source_versions v ON v.source_id=h.id AND v.generation=h.generation WHERE h.resource_id=$1 FOR UPDATE OF h;
-- name: CreateHostingBuildSource :exec
INSERT INTO hosting_build_sources(id,resource_id,project_id,generation) VALUES($1,$2,$3,1);
-- name: AdvanceHostingBuildSource :execrows
UPDATE hosting_build_sources SET generation=generation+1 WHERE id=$1 AND generation=$2;
-- name: AppendHostingBuildSourceVersion :exec
INSERT INTO hosting_build_source_versions(source_id,generation,body,body_hash,created_by) VALUES($1,$2,$3,$4,$5);
-- name: CreateHostingSourceGrant :exec
INSERT INTO hosting_source_grants(id,version,source_id,resource_id,project_id,secret_id,secret_version,kind,repository_url,policy_hash,created_by) VALUES($1,1,$2,$3,$4,$5,$6,$7,$8,$9,$10);
-- name: LockHostingSourceGrant :one
SELECT * FROM hosting_source_grants WHERE id=$1 FOR UPDATE;
-- name: RevokeHostingSourceGrant :execrows
UPDATE hosting_source_grants SET revoked_at=clock_timestamp() WHERE id=$1 AND revoked_at IS NULL;
-- name: LockHostingBuildSecretVersion :one
SELECT s.id FROM project_managed_secrets s JOIN project_managed_secret_versions v ON v.secret_id=s.id WHERE s.id=$1 AND s.project_id=$2 AND v.version=$3 AND s.status='active' FOR SHARE OF s;
-- name: HostingBuildTarget :one
SELECT b.id AS binding_id,b.destination_id,d.server_id,s.runner_id FROM resource_bindings b JOIN destinations d ON d.id=b.destination_id JOIN servers s ON s.id=d.server_id WHERE b.resource_id=$1;
-- name: LockHostingBuildRunner :one
SELECT r.id FROM runners r WHERE r.id=$1 AND r.status='active' AND r.token_hash<>'' AND 'hosting_build_v1'=ANY(r.capabilities) AND NOT('shell'=ANY(r.capabilities)) AND EXISTS(SELECT 1 FROM runner_enrollments e WHERE e.runner_id=r.id AND e.used_at IS NOT NULL AND e.revoked_at IS NULL AND e.credential_hash=r.token_hash) FOR SHARE OF r;
-- name: HostingBuildObservedDaemon :one
SELECT coalesce(daemon_id,'')::text FROM hosting_binding_state WHERE binding_id=$1 FOR SHARE;
-- name: HostingBuildIdempotency :one
SELECT id,request_hash FROM hosting_builds WHERE resource_id=$1 AND idempotency_key=$2;
-- name: InsertHostingBuild :exec
INSERT INTO hosting_builds(id,resource_id,project_id,source_id,source_generation,idempotency_key,request_hash,snapshot,snapshot_hash,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10);
-- name: GetHostingBuildDocument :one
SELECT jsonb_build_object('id',b.id,'project_id',b.project_id,'resource_id',b.resource_id,'state',b.state,'run_id',coalesce(b.run_id,''),'daemon_id',b.daemon_id,'revision',b.revision,'created_by',b.created_by,'snapshot',b.snapshot,'source_receipt',sr.body,'artifact',a.body,'failure_code',b.failure_code) FROM hosting_builds b LEFT JOIN hosting_build_receipts sr ON sr.build_id=b.id AND sr.phase='source' LEFT JOIN hosting_build_artifacts a ON a.build_id=b.id WHERE b.id=$1;
-- name: LockHostingBuildState :one
SELECT revision FROM hosting_builds WHERE id=$1 FOR UPDATE;
-- name: SetHostingBuildState :execrows
UPDATE hosting_builds SET state=$3,revision=revision+1,updated_at=clock_timestamp(),failure_code=$4 WHERE id=$1 AND revision=$2;
-- name: PinHostingBuildDaemon :execrows
UPDATE hosting_builds SET daemon_id=$3,state='building',revision=revision+1,updated_at=clock_timestamp() WHERE id=$1 AND revision=$2 AND state IN('sourcing','building') AND (daemon_id='' OR daemon_id=$3);
-- name: BindHostingBuildRun :execrows
UPDATE hosting_builds SET run_id=$3,revision=revision+1,updated_at=clock_timestamp() WHERE id=$1 AND revision=$2 AND state='queued' AND run_id IS NULL;
-- name: InsertHostingBuildTask :exec
INSERT INTO hosting_build_tasks(build_id,run_id,runner_id) VALUES($1,$2,$3);
-- name: ValidateHostingBuildTask :one
SELECT r.id FROM task_runs r WHERE r.id=$1 AND r.project_id=$2 AND r.status='queued' AND r.runner_id IS NULL AND r.requested_by=$3 AND r.run_spec=jsonb_build_object('type','hosting_build_v1','hosting_build_id',$4::text) AND r.xmin::text::bigint=mod(txid_current(),4294967296) AND r.workflow IN('{}'::jsonb,'{"steps":[]}'::jsonb) FOR UPDATE;
-- name: CheckHostingBuildAuthority :one
SELECT l.id FROM run_leases l JOIN hosting_build_tasks t ON t.run_id=l.run_id JOIN task_runs r ON r.id=l.run_id WHERE t.build_id=$1 AND t.runner_id=$2 AND l.runner_id=$2 AND l.run_id=$3 AND l.id=$4 AND l.attempt=$5 AND l.fence=$6 AND r.project_id=$7 AND r.run_spec=jsonb_build_object('type','hosting_build_v1','hosting_build_id',$1::text) AND (NOT $8::boolean OR (l.status='active' AND l.expires_at>clock_timestamp() AND r.status='running' AND r.runner_id=$2)) FOR UPDATE OF l;
-- name: GetHostingBuildReceipt :one
SELECT receipt_id,authority,body_hash FROM hosting_build_receipts WHERE build_id=$1 AND phase=$2;
-- name: InsertHostingBuildReceipt :exec
INSERT INTO hosting_build_receipts(build_id,phase,receipt_id,authority,body,body_hash) VALUES($1,$2,$3,$4,$5,$6);
-- name: InsertHostingBuildArtifact :exec
INSERT INTO hosting_build_artifacts(build_id,body,body_hash) VALUES($1,$2,$3);

-- name: GetHostingSourceGrant :one
SELECT * FROM hosting_source_grants WHERE id=$1;
-- name: LockHostingBuildEnrollment :one
SELECT e.id FROM runner_enrollments e JOIN runners r ON r.id=e.runner_id WHERE e.runner_id=$1 AND e.used_at IS NOT NULL AND e.revoked_at IS NULL AND e.credential_hash=r.token_hash FOR SHARE OF e;
-- name: HostingBuildTaskExists :one
SELECT EXISTS(SELECT 1 FROM task_runs WHERE id=$1);
