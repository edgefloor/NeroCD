-- name: SchedulerSchemaCompatible :one
SELECT nerocd_scheduler_schema_compatible()
   AND nerocd_replay_schema_compatible()
   AND nerocd_enrollment_schema_compatible() AS compatible;
