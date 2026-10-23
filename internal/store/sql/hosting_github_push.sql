-- name: LockGitHubPushQuota :exec
SELECT pg_advisory_xact_lock(hashtextextended('hosting-github-push-quotas',0));
-- name: ReadGitHubPushPolicy :one
SELECT * FROM hosting_github_push_policies WHERE resource_id=$1;
-- name: ReadGitHubPushPolicyByID :one
SELECT * FROM hosting_github_push_policies WHERE id=$1;
-- name: LockGitHubPushPolicy :one
SELECT * FROM hosting_github_push_policies WHERE resource_id=$1 FOR UPDATE;
-- name: InsertGitHubPushPolicy :exec
INSERT INTO hosting_github_push_policies(id,resource_id,project_id,generation,body) VALUES($1,$2,$3,$4,$5);
-- name: UpdateGitHubPushPolicy :exec
UPDATE hosting_github_push_policies SET generation=$2,body=$3 WHERE id=$1;
-- name: SetGitHubPushVerification :exec
UPDATE hosting_github_push_policies SET verification_token=$2,verification_expires_at=clock_timestamp()+interval '30 seconds' WHERE id=$1;
-- name: ReadGitHubPushCommand :one
SELECT * FROM hosting_github_push_commands WHERE resource_id=$1 AND request_id=$2;
-- name: InsertGitHubPushCommand :exec
INSERT INTO hosting_github_push_commands(resource_id,request_id,actor_id,kind,body_hash,expected_generation,token) VALUES($1,$2,$3,$4,$5,$6,$7);
-- name: FinishGitHubPushCommand :exec
UPDATE hosting_github_push_commands SET receipt=$3 WHERE resource_id=$1 AND request_id=$2;
-- name: ReadGitHubPushDelivery :one
SELECT * FROM hosting_github_push_deliveries WHERE id=$1;
-- name: FindGitHubPushDelivery :one
SELECT * FROM hosting_github_push_deliveries WHERE policy_id=$1 AND delivery_id=$2;
-- name: LockGitHubPushDelivery :one
SELECT * FROM hosting_github_push_deliveries WHERE id=$1 FOR UPDATE;
-- name: InsertGitHubPushDelivery :one
INSERT INTO hosting_github_push_deliveries(id,policy_id,policy_generation,resource_id,project_id,delivery_id,body_hash,after_commit,state) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING *;
-- name: ClaimGitHubPushDelivery :one
UPDATE hosting_github_push_deliveries SET state='verifying',token=$2,token_expires_at=clock_timestamp()+interval '30 seconds',attempts=attempts+1,next_retry_at=clock_timestamp()+interval '30 seconds' WHERE id=$1 AND attempts<32 AND expires_at>clock_timestamp() RETURNING *;
-- name: FinishGitHubPushDelivery :one
UPDATE hosting_github_push_deliveries SET state=$2,reason=$3,verified_commit=$4,build_id=$5,token='',token_expires_at=NULL,next_retry_at=clock_timestamp()+make_interval(secs=>sqlc.arg(retry_seconds)::integer) WHERE id=$1 RETURNING *;
-- name: RetryGitHubPushDelivery :one
UPDATE hosting_github_push_deliveries SET state='received',reason='',attempts=0,token='',token_expires_at=NULL,expires_at=clock_timestamp()+interval '24 hours',retain_until=clock_timestamp()+interval '72 hours',next_retry_at=clock_timestamp() WHERE id=$1 RETURNING *;
-- name: ObsoleteGitHubPushDeliveries :exec
UPDATE hosting_github_push_deliveries SET state='obsolete',reason='policy_changed',token='',token_expires_at=NULL WHERE policy_id=$1 AND state IN('received','verifying','retry_wait');
-- name: GitHubPushCounts :one
SELECT count(*)::integer AS retained_resource,count(*) FILTER(WHERE d.state IN('received','verifying','retry_wait'))::integer AS pending_resource,(SELECT count(*)::integer FROM hosting_github_push_deliveries p WHERE p.project_id=sqlc.arg(project_id)) AS retained_project,(SELECT count(*)::integer FROM hosting_github_push_deliveries p WHERE p.project_id=sqlc.arg(project_id) AND p.state IN('received','verifying','retry_wait')) AS pending_project FROM hosting_github_push_deliveries d WHERE d.resource_id=sqlc.arg(resource_id);
-- name: EvictGitHubPushResourceReceipt :execrows
DELETE FROM hosting_github_push_deliveries d WHERE d.id=(SELECT p.id FROM hosting_github_push_deliveries p WHERE p.resource_id=sqlc.arg(resource_id) AND p.state NOT IN('received','verifying','retry_wait') ORDER BY p.received_at,p.id LIMIT 1 FOR UPDATE SKIP LOCKED);
-- name: EvictGitHubPushProjectReceipt :execrows
DELETE FROM hosting_github_push_deliveries d WHERE d.id=(SELECT p.id FROM hosting_github_push_deliveries p WHERE p.project_id=sqlc.arg(project_id) AND p.state NOT IN('received','verifying','retry_wait') ORDER BY p.received_at,p.id LIMIT 1 FOR UPDATE SKIP LOCKED);
-- name: ListGitHubPushDeliveries :many
SELECT d.* FROM hosting_github_push_deliveries d WHERE d.policy_id=sqlc.arg(policy_id) AND (sqlc.arg(cursor)::text='' OR (d.received_at,d.id)<(SELECT p.received_at,p.id FROM hosting_github_push_deliveries p WHERE p.policy_id=sqlc.arg(policy_id) AND p.delivery_id=sqlc.arg(cursor))) ORDER BY d.received_at DESC,d.id DESC LIMIT sqlc.arg(page_limit)::integer;
-- name: ReadGitHubPushCommitIntent :one
SELECT build_id FROM hosting_github_push_commit_intents WHERE policy_id=$1 AND policy_generation=$2 AND source_generation=$3 AND commit_sha=$4;
-- name: InsertGitHubPushCommitIntent :exec
INSERT INTO hosting_github_push_commit_intents(policy_id,policy_generation,source_generation,commit_sha,build_id) VALUES($1,$2,$3,$4,$5);
-- name: GitHubPushAuditExists :one
SELECT EXISTS(SELECT 1 FROM audit_events WHERE id=$1);
-- name: ReadGitHubPushSecret :one
SELECT v.* FROM project_managed_secret_versions v JOIN project_managed_secrets s ON s.id=v.secret_id WHERE s.project_id=$1 AND s.id=$2 AND v.version=$3 AND s.status='active' FOR SHARE OF s;
-- name: RecoverGitHubPushDeliveries :many
SELECT id FROM hosting_github_push_deliveries WHERE state IN('received','verifying','retry_wait') AND next_retry_at<=clock_timestamp() ORDER BY next_retry_at,id LIMIT 8 FOR UPDATE SKIP LOCKED;
-- name: TouchGitHubPushDelivery :exec
UPDATE hosting_github_push_deliveries SET next_retry_at=clock_timestamp()+interval '30 seconds' WHERE id=$1;
-- name: ExpireGitHubPushDeliveries :exec
UPDATE hosting_github_push_deliveries SET state='blocked_exhausted',reason='blocked_exhausted',token='',token_expires_at=NULL WHERE id IN(SELECT id FROM hosting_github_push_deliveries WHERE state IN('received','verifying','retry_wait') AND (expires_at<=clock_timestamp() OR attempts>=32 AND next_retry_at<=clock_timestamp()) ORDER BY next_retry_at,id LIMIT 8 FOR UPDATE SKIP LOCKED);
-- name: PurgeGitHubPushReceipts :exec
DELETE FROM hosting_github_push_deliveries WHERE id IN(SELECT id FROM hosting_github_push_deliveries WHERE state NOT IN('received','verifying','retry_wait') AND retain_until<=clock_timestamp() ORDER BY retain_until,id LIMIT 8 FOR UPDATE SKIP LOCKED);
