-- name: ListServices :many
SELECT * FROM services WHERE sqlc.arg(project_id)::text='' OR project_id=sqlc.arg(project_id) ORDER BY name;
-- name: GetServiceByID :one
SELECT * FROM services WHERE id=$1;
-- name: CreateService :one
INSERT INTO services(id,project_id,name,repository_id,compose_path,profiles,owner_id,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING *;
-- name: ListEnvironments :many
SELECT * FROM environments WHERE sqlc.arg(service_id)::text='' OR service_id=sqlc.arg(service_id) ORDER BY name;
-- name: GetEnvironmentByID :one
SELECT * FROM environments WHERE id=$1;
-- name: CreateEnvironment :one
INSERT INTO environments(id,service_id,name,runner_selector,compose_project,health_policy,confirmation_required,timeout_seconds,secret_bindings,rollback_safe,current_healthy_revision_id,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) RETURNING *;
-- name: ListRevisions :many
SELECT * FROM revisions WHERE sqlc.arg(service_id)::text='' OR service_id=sqlc.arg(service_id) ORDER BY created_at DESC;
-- name: CreateRevision :one
INSERT INTO revisions(id,service_id,requested_ref,git_commit,compose_hash,image_digests,content_identity,created_by,created_at,provenance_state)
VALUES(sqlc.arg(id),sqlc.arg(service_id),sqlc.arg(requested_ref),sqlc.arg(git_commit),sqlc.arg(compose_hash),COALESCE(sqlc.arg(image_digests)::text[], ARRAY[]::text[]),sqlc.arg(content_identity),sqlc.arg(created_by),sqlc.arg(created_at),CASE WHEN sqlc.arg(git_commit)='' AND sqlc.arg(compose_hash)='' AND cardinality(COALESCE(sqlc.arg(image_digests)::text[], ARRAY[]::text[]))=0 AND sqlc.arg(content_identity)='' THEN 'pending' ELSE 'legacy_unverified' END) RETURNING *;
-- name: GetRevisionByIdentity :one
SELECT * FROM revisions WHERE service_id=$1 AND content_identity=$2;
-- name: ListDeployments :many
SELECT * FROM deployments WHERE sqlc.arg(environment_id)::text='' OR environment_id=sqlc.arg(environment_id) ORDER BY created_at DESC;
-- name: GetDeploymentByID :one
SELECT * FROM deployments WHERE id=$1;
-- name: CreateDeployment :one
INSERT INTO deployments(id,environment_id,desired_revision_id,previous_healthy_revision_id,task_run_id,idempotency_key,status,requested_by,created_at,updated_at,fence_required,rollback_of_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) RETURNING *;
-- name: CreateRollbackDeploymentIfAbsent :one
INSERT INTO deployments(id,environment_id,desired_revision_id,previous_healthy_revision_id,task_run_id,idempotency_key,status,requested_by,created_at,updated_at,fence_required,rollback_of_id)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
ON CONFLICT DO NOTHING
RETURNING *;
-- name: GetDeploymentByEnvironmentKey :one
SELECT * FROM deployments WHERE environment_id=$1 AND idempotency_key=$2;

-- name: LockDeploymentByID :one
SELECT * FROM deployments WHERE id=$1 FOR UPDATE;

-- name: GetDeploymentCancellation :one
SELECT deployment_id, request_id, actor_id FROM deployment_cancellations WHERE deployment_id=$1 FOR KEY SHARE;

-- name: CreateDeploymentCancellation :exec
INSERT INTO deployment_cancellations(deployment_id,request_id,actor_id) VALUES($1,$2,$3);

-- name: CancelDeploymentBeforeApply :one
UPDATE deployments SET status='canceled', finished_at=clock_timestamp(), updated_at=clock_timestamp()
WHERE id=$1 AND status IN ('queued','waiting_confirmation','assigned','preparing') RETURNING *;

-- name: RequestDeploymentCancellation :one
UPDATE deployments SET status='cancel_requested', updated_at=clock_timestamp()
WHERE id=$1 AND status IN ('applying','verifying') RETURNING *;

-- name: CancelDeploymentRun :execrows
UPDATE task_runs SET status='canceled', runner_id=NULL, finished_at=clock_timestamp()
WHERE id=$1 AND status NOT IN ('succeeded','failed','canceled');

-- name: CancelDeploymentActiveLeases :exec
UPDATE run_leases SET status='canceled', completed_at=clock_timestamp()
WHERE run_id=$1 AND status='active';

-- name: CancelDeploymentActiveAttempts :exec
UPDATE deployment_attempts SET status='canceled', finished_at=clock_timestamp()
WHERE deployment_id=$1 AND status='active';

-- name: DeploymentEnvironmentID :one
SELECT environment_id FROM deployments WHERE id=$1;

-- name: LockRollbackEnvironment :one
SELECT rollback_safe,current_healthy_revision_id FROM environments WHERE id=$1 FOR UPDATE;

-- name: DeploymentProjectID :one
SELECT s.project_id FROM services s JOIN environments e ON e.service_id=s.id WHERE e.id=$1;
-- name: UpdateEnvironmentHealthyRevision :exec
UPDATE environments SET current_healthy_revision_id=$2 WHERE id=$1;

-- name: CreateDeploymentRun :one
INSERT INTO task_runs (id,project_id,template_id,run_spec,workflow,workflow_state,runner_tags,status,requested_by,started_at,finished_at,created_at,claim_order_at)
VALUES ($1,$2,NULL,$3,'{"steps":[]}'::jsonb,'{"steps":[]}'::jsonb,$4,$5,$6,clock_timestamp(),NULL,clock_timestamp(),clock_timestamp())
RETURNING *;

-- name: LockDeploymentForRun :one
SELECT d.*
FROM deployments d
WHERE d.task_run_id = $1
FOR UPDATE;

-- name: CreateDeploymentAttemptForLease :one
INSERT INTO deployment_attempts(deployment_id,run_id,lease_id,runner_id,attempt,fence)
SELECT d.id, d.task_run_id, $2, $3, $4, $5
FROM deployments d
WHERE d.task_run_id=$1
RETURNING *;

-- name: AssignDeploymentForLease :one
UPDATE deployments
SET status='assigned', updated_at=clock_timestamp()
WHERE task_run_id=$1 AND status='queued'
RETURNING *;

-- name: LockDeploymentAttempt :one
SELECT da.*
FROM deployment_attempts da
WHERE da.deployment_id=$1 AND da.run_id=$2 AND da.lease_id=$3
  AND da.runner_id=$4 AND da.attempt=$5 AND da.fence=$6
FOR UPDATE;

-- name: GetDeploymentTransitionReplay :one
SELECT * FROM deployment_transitions
WHERE deployment_id=$1 AND transition_key=$2;

-- name: CreateDeploymentTransitionReplay :exec
INSERT INTO deployment_transitions(deployment_id,attempt,transition_key,expected_status,target_status,health_passed,failure_code,metadata)
VALUES($1,$2,$3,$4,$5,$6,$7,$8);

-- name: FencedTransitionDeployment :one
UPDATE deployments
SET status=$3, health_passed=$4, failure_code=$5, updated_at=clock_timestamp(),
    finished_at=CASE WHEN sqlc.arg(terminal)::boolean THEN clock_timestamp() ELSE NULL END
WHERE id=$1 AND status=$2
RETURNING *;

-- name: FinishRollbackSource :one
UPDATE deployments
SET status=$2, updated_at=clock_timestamp(), finished_at=clock_timestamp()
WHERE id=$1 AND status='rolling_back'
RETURNING *;

-- name: CommitDeploymentHealthyRevision :exec
UPDATE environments
SET current_healthy_revision_id=$2
WHERE id=$1;

-- name: ConfirmDeployment :one
UPDATE deployments
SET status='assigned', confirmed_by=$2, updated_at=clock_timestamp()
WHERE id=$1 AND status='waiting_confirmation'
RETURNING *;

-- name: FailPreAssignmentDeployment :one
UPDATE deployments
SET status='failed', failure_code=$2, updated_at=clock_timestamp(), finished_at=clock_timestamp()
WHERE id=$1 AND status IN ('queued','waiting_confirmation')
RETURNING *;

-- name: FailPreAssignmentDeploymentRun :execrows
UPDATE task_runs
SET status='failed', runner_id=NULL, finished_at=clock_timestamp()
WHERE id=$1 AND status IN ('queued','waiting_approval');

-- name: QueueConfirmedDeploymentRun :exec
UPDATE task_runs
SET status='queued', claim_order_at=clock_timestamp()
WHERE id=$1 AND status='waiting_approval';

-- name: FinishDeploymentAttempt :exec
UPDATE deployment_attempts
SET status=$3, finished_at=clock_timestamp()
WHERE deployment_id=$1 AND attempt=$2;

-- name: CompleteDeploymentLease :one
UPDATE run_leases
SET status=$6, completed_at=clock_timestamp()
WHERE id=$1 AND run_id=$2 AND runner_id=$3 AND attempt=$4 AND fence=$5
  AND status='active' AND expires_at > clock_timestamp()
RETURNING *;

-- name: CompleteDeploymentRun :execrows
UPDATE task_runs
SET status=$3, runner_id=NULL, finished_at=clock_timestamp()
WHERE id=$1 AND runner_id=$2 AND status='running';

-- name: IsDeploymentRun :one
SELECT EXISTS(SELECT 1 FROM deployments WHERE task_run_id=$1) AS is_deployment_run;

-- name: DeploymentPlan :one
SELECT d.id AS deployment_id,d.status AS deployment_status,d.task_run_id,da.lease_id,da.attempt,da.fence,s.project_id,s.id AS service_id,e.id AS environment_id,r.id AS repository_id,r.url,r.repository_policy,rv.requested_ref,rv.git_commit,s.compose_path,s.profiles,e.compose_project,e.timeout_seconds,e.health_policy,e.secret_bindings,e.rollback_safe,d.previous_healthy_revision_id,d.rollback_of_id,dc.request_id AS cancellation_request_id
FROM deployments d JOIN deployment_attempts da ON da.deployment_id=d.id AND da.run_id=d.task_run_id
JOIN run_leases l ON l.id=da.lease_id JOIN environments e ON e.id=d.environment_id JOIN services s ON s.id=e.service_id
JOIN repositories r ON r.id=s.repository_id JOIN revisions rv ON rv.id=d.desired_revision_id
LEFT JOIN deployment_cancellations dc ON dc.deployment_id=d.id
WHERE d.id=$1 AND d.task_run_id=$2 AND da.lease_id=$3 AND l.runner_id=$4 AND da.runner_id=$4 AND da.attempt=$5 AND da.fence=$6 AND l.status='active' AND l.expires_at>clock_timestamp();

-- name: LockRevisionForProvenance :one
SELECT rv.* FROM deployments d JOIN revisions rv ON rv.id=d.desired_revision_id JOIN deployment_attempts da ON da.deployment_id=d.id AND da.run_id=d.task_run_id JOIN run_leases l ON l.id=da.lease_id
WHERE d.id=$1 AND d.task_run_id=$2 AND da.lease_id=$3 AND da.runner_id=$4 AND l.runner_id=$4 AND da.attempt=$5 AND da.fence=$6 AND l.status='active' AND l.expires_at>clock_timestamp() AND d.status IN ('assigned','preparing','applying','verifying') FOR UPDATE;

-- name: ResolveRevisionProvenance :one
UPDATE revisions SET git_commit=$2,compose_hash=$3,image_digests=$4,content_identity=$2 || ':' || $3,provenance_state='resolved',provenance_resolved=true,resolved_at=clock_timestamp() WHERE id=$1 RETURNING *;

-- name: IsRevisionProvenanceResolved :one
SELECT provenance_state='resolved' FROM revisions WHERE id=$1 FOR KEY SHARE;

-- name: GetProvenanceResolutionReplay :one
SELECT r.*, p.run_id, p.lease_id, p.runner_id, p.attempt, p.fence,
 p.git_commit AS replay_git_commit, p.compose_hash AS replay_compose_hash,
 p.image_digests AS replay_image_digests
FROM provenance_resolutions p JOIN revisions r ON r.id=p.revision_id
WHERE p.deployment_id=$1 AND p.resolution_id=$2 FOR KEY SHARE;

-- name: LockProvenanceAttempt :one
SELECT rv.*
FROM deployments d JOIN deployment_attempts da ON da.deployment_id=d.id
JOIN revisions rv ON rv.id=d.desired_revision_id
WHERE d.id=$1 AND da.run_id=$2 AND da.lease_id=$3 AND da.runner_id=$4 AND da.attempt=$5 AND da.fence=$6
FOR UPDATE OF d, da, rv;

-- name: ProvenanceAttemptIsActive :one
SELECT l.status='active' AND l.expires_at>clock_timestamp() AND d.status IN ('assigned','preparing','applying','verifying')
FROM run_leases l JOIN deployments d ON d.id=$1
WHERE l.id=$2 AND l.run_id=$3 AND l.runner_id=$4 AND l.attempt=$5 AND l.fence=$6 FOR UPDATE OF l;

-- name: CreateProvenanceResolutionReplay :exec
INSERT INTO provenance_resolutions(deployment_id,attempt,resolution_id,run_id,lease_id,runner_id,fence,revision_id,git_commit,compose_hash,image_digests,content_identity,audit_id)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13);
