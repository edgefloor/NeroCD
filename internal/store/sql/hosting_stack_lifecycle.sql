-- name: ReadHostingStackStopPlan :one
SELECT plan FROM hosting_stack_stop_plans WHERE operation_id=$1;
-- name: CreateHostingStackStopPlan :exec
INSERT INTO hosting_stack_stop_plans(operation_id,target_operation_id,plan,plan_hash) VALUES($1,$2,$3,$4);
-- name: ReadHostingStackStopped :one
SELECT stopped FROM hosting_stack_lifecycle WHERE binding_id=$1;
-- name: SetHostingStackStopped :exec
INSERT INTO hosting_stack_lifecycle(binding_id,stopped) VALUES($1,$2) ON CONFLICT(binding_id) DO UPDATE SET stopped=excluded.stopped;
-- name: ClearHostingStackStopped :execrows
UPDATE hosting_stack_lifecycle SET stopped=NULL WHERE binding_id=$1 AND stopped->>'stop_operation_id'=$2;
-- name: CreateHostingStackStopTaskTarget :exec
INSERT INTO hosting_task_targets(run_id,operation_id,binding_id,runner_id,mode) VALUES($1,$2,$3,$4,'stack_stop');
-- name: StopHostingStackReleaseWriters :exec
UPDATE hosting_retained_releases SET retained_running=false WHERE operation_id=$1;
-- name: EndHostingStackLogDemand :exec
WITH stamp AS MATERIALIZED(SELECT clock_timestamp() AS now)
UPDATE hosting_runtime_log_sessions SET state='target_changed',ended_at=stamp.now,demand_expires_at=LEAST(demand_expires_at,stamp.now),epoch_expires_at=NULL,purge_after=LEAST(purge_after,stamp.now+interval '5 minutes') FROM stamp WHERE resource_id=$1 AND ended_at IS NULL;
