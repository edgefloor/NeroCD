-- name: SchedulerSchemaCompatible :one
SELECT nerocd_scheduler_schema_compatible()
   AND nerocd_replay_schema_compatible()
   AND nerocd_enrollment_schema_compatible()
   AND nerocd_deployment_schema_compatible()
AND nerocd_provenance_schema_compatible()
AND nerocd_repository_policy_schema_compatible()
AND nerocd_identity_schema_compatible()
AND nerocd_observability_schema_compatible()
AND nerocd_retention_schema_compatible() AS compatible;
