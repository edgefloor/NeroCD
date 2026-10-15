-- name: CountAuditEvents :one
SELECT count(*) FROM audit_events;

-- name: ListAuditEventsPage :many
SELECT * FROM audit_events ORDER BY created_at DESC, id DESC LIMIT $1 OFFSET $2;

-- Go strings.TrimSpace uses Unicode White Space. Keep this explicit btrim set
-- in both scoped queries so metadata and resource targets resolve identically
-- in the memory and PostgreSQL repositories.
-- name: CountAuditEventsForScope :one
SELECT count(*)
FROM audit_events AS event
LEFT JOIN task_runs AS audit_run
  ON starts_with(event.target_id, 'run_')
 AND audit_run.id=btrim(event.target_id, U&'\0009\000A\000B\000C\000D\0020\0085\00A0\1680\2000\2001\2002\2003\2004\2005\2006\2007\2008\2009\200A\2028\2029\202F\205F\3000')
LEFT JOIN task_templates AS audit_template
  ON starts_with(event.target_id, 'tpl_')
 AND audit_template.id=btrim(event.target_id, U&'\0009\000A\000B\000C\000D\0020\0085\00A0\1680\2000\2001\2002\2003\2004\2005\2006\2007\2008\2009\200A\2028\2029\202F\205F\3000')
LEFT JOIN repositories AS audit_repository
  ON starts_with(event.target_id, 'repo_')
 AND audit_repository.id=btrim(event.target_id, U&'\0009\000A\000B\000C\000D\0020\0085\00A0\1680\2000\2001\2002\2003\2004\2005\2006\2007\2008\2009\200A\2028\2029\202F\205F\3000')
WHERE (sqlc.arg(actor_id)::text<>'' AND event.actor_id=sqlc.arg(actor_id))
   OR CASE
        WHEN jsonb_typeof(event.metadata->'project_id')='string'
         AND btrim(event.metadata->>'project_id', U&'\0009\000A\000B\000C\000D\0020\0085\00A0\1680\2000\2001\2002\2003\2004\2005\2006\2007\2008\2009\200A\2028\2029\202F\205F\3000')<>''
          THEN btrim(event.metadata->>'project_id', U&'\0009\000A\000B\000C\000D\0020\0085\00A0\1680\2000\2001\2002\2003\2004\2005\2006\2007\2008\2009\200A\2028\2029\202F\205F\3000')=ANY(sqlc.arg(project_ids)::text[])
        WHEN starts_with(event.target_id, 'proj_')
          THEN event.target_id=ANY(sqlc.arg(project_ids)::text[])
        WHEN starts_with(event.target_id, 'run_')
          THEN audit_run.project_id=ANY(sqlc.arg(project_ids)::text[])
        WHEN starts_with(event.target_id, 'tpl_')
          THEN audit_template.project_id=ANY(sqlc.arg(project_ids)::text[])
        WHEN starts_with(event.target_id, 'repo_')
          THEN audit_repository.project_id=ANY(sqlc.arg(project_ids)::text[])
        ELSE false
      END;

-- name: ListAuditEventsPageForScope :many
SELECT event.*
FROM audit_events AS event
LEFT JOIN task_runs AS audit_run
  ON starts_with(event.target_id, 'run_')
 AND audit_run.id=btrim(event.target_id, U&'\0009\000A\000B\000C\000D\0020\0085\00A0\1680\2000\2001\2002\2003\2004\2005\2006\2007\2008\2009\200A\2028\2029\202F\205F\3000')
LEFT JOIN task_templates AS audit_template
  ON starts_with(event.target_id, 'tpl_')
 AND audit_template.id=btrim(event.target_id, U&'\0009\000A\000B\000C\000D\0020\0085\00A0\1680\2000\2001\2002\2003\2004\2005\2006\2007\2008\2009\200A\2028\2029\202F\205F\3000')
LEFT JOIN repositories AS audit_repository
  ON starts_with(event.target_id, 'repo_')
 AND audit_repository.id=btrim(event.target_id, U&'\0009\000A\000B\000C\000D\0020\0085\00A0\1680\2000\2001\2002\2003\2004\2005\2006\2007\2008\2009\200A\2028\2029\202F\205F\3000')
WHERE (sqlc.arg(actor_id)::text<>'' AND event.actor_id=sqlc.arg(actor_id))
   OR CASE
        WHEN jsonb_typeof(event.metadata->'project_id')='string'
         AND btrim(event.metadata->>'project_id', U&'\0009\000A\000B\000C\000D\0020\0085\00A0\1680\2000\2001\2002\2003\2004\2005\2006\2007\2008\2009\200A\2028\2029\202F\205F\3000')<>''
          THEN btrim(event.metadata->>'project_id', U&'\0009\000A\000B\000C\000D\0020\0085\00A0\1680\2000\2001\2002\2003\2004\2005\2006\2007\2008\2009\200A\2028\2029\202F\205F\3000')=ANY(sqlc.arg(project_ids)::text[])
        WHEN starts_with(event.target_id, 'proj_')
          THEN event.target_id=ANY(sqlc.arg(project_ids)::text[])
        WHEN starts_with(event.target_id, 'run_')
          THEN audit_run.project_id=ANY(sqlc.arg(project_ids)::text[])
        WHEN starts_with(event.target_id, 'tpl_')
          THEN audit_template.project_id=ANY(sqlc.arg(project_ids)::text[])
        WHEN starts_with(event.target_id, 'repo_')
          THEN audit_repository.project_id=ANY(sqlc.arg(project_ids)::text[])
        ELSE false
      END
ORDER BY event.created_at DESC, event.id DESC
LIMIT sqlc.arg(page_limit) OFFSET sqlc.arg(page_offset);

-- name: CreateAuditEvent :exec
INSERT INTO audit_events (id,actor_id,action,target_id,metadata,created_at)
VALUES ($1,$2,$3,$4,$5,$6);

-- name: CreateAuditEventAtDatabaseTime :exec
INSERT INTO audit_events (id,actor_id,action,target_id,metadata,created_at)
VALUES ($1,$2,$3,$4,$5,clock_timestamp());

-- name: GetAuditEventByID :one
SELECT * FROM audit_events WHERE id=$1;

-- name: CreateSecretAccessAudit :one
INSERT INTO audit_events (id,actor_id,action,target_id,metadata,created_at)
VALUES ($1,$2,'secret.access',$3,$4,clock_timestamp())
RETURNING *;

-- name: GetRunContextForSecretAccess :one
SELECT run_spec,workflow,workflow_state FROM task_runs WHERE id=$1;
