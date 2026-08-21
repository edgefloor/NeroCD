-- A resolution acknowledgement is durable and bound to the complete fenced
-- authority, so an exact retry can be replayed after terminal completion while
-- a stale or altered request cannot borrow that acknowledgement.
CREATE TABLE provenance_resolutions (
 deployment_id TEXT NOT NULL,
 attempt INTEGER NOT NULL,
 resolution_id TEXT NOT NULL,
 run_id TEXT NOT NULL,
 lease_id TEXT NOT NULL,
 runner_id TEXT NOT NULL,
 fence TEXT NOT NULL,
 revision_id TEXT NOT NULL REFERENCES revisions(id),
 git_commit TEXT NOT NULL,
 compose_hash TEXT NOT NULL,
 image_digests TEXT[] NOT NULL,
 content_identity TEXT NOT NULL,
 audit_id TEXT NOT NULL REFERENCES audit_events(id),
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY (deployment_id, resolution_id),
 FOREIGN KEY (deployment_id, attempt) REFERENCES deployment_attempts(deployment_id, attempt) ON DELETE CASCADE,
 -- These duplicate keys make the record's stored authority independently
 -- checkable and prevent a replay from being re-bound to another lease/run.
 FOREIGN KEY (lease_id) REFERENCES run_leases(id) ON DELETE RESTRICT,
 UNIQUE (deployment_id, attempt, resolution_id),
 UNIQUE (revision_id),
 CHECK (deployment_id<>''), CHECK (run_id<>''), CHECK (lease_id<>''),
 CHECK (runner_id<>''), CHECK (attempt>0), CHECK (fence<>''),
 CHECK (resolution_id<>''), CHECK (git_commit ~ '^[0-9a-f]{40,64}$'),
 CHECK (compose_hash ~ '^sha256:[0-9a-f]{64}$'),
 CHECK (content_identity=git_commit || ':' || compose_hash),
 CHECK (nerocd_sha256_digests(image_digests))
);

CREATE INDEX provenance_resolutions_exact_authority
 ON provenance_resolutions(deployment_id, resolution_id, run_id, lease_id, runner_id, attempt, fence);
CREATE INDEX provenance_resolutions_attempt_revision
 ON provenance_resolutions(deployment_id, attempt, revision_id);

-- A row must name the exact deployment attempt and lease authority that it
-- claims.  The compound relationships cross tables that do not have a single
-- natural FK, so enforce them under the same row locks used by the resolver.
CREATE OR REPLACE FUNCTION nerocd_validate_provenance_resolution() RETURNS trigger LANGUAGE plpgsql AS $function$
DECLARE actual_run TEXT; actual_lease TEXT; actual_runner TEXT; actual_fence TEXT; desired TEXT;
BEGIN
 SELECT da.run_id, da.lease_id, da.runner_id, da.fence, d.desired_revision_id
 INTO actual_run, actual_lease, actual_runner, actual_fence, desired
 FROM deployment_attempts da JOIN deployments d ON d.id=da.deployment_id
 WHERE da.deployment_id=NEW.deployment_id AND da.attempt=NEW.attempt FOR KEY SHARE;
 IF NOT FOUND OR actual_run IS DISTINCT FROM NEW.run_id OR actual_lease IS DISTINCT FROM NEW.lease_id
    OR actual_runner IS DISTINCT FROM NEW.runner_id OR actual_fence IS DISTINCT FROM NEW.fence
    OR desired IS DISTINCT FROM NEW.revision_id THEN
   RAISE EXCEPTION 'provenance resolution does not match exact deployment attempt authority';
 END IF;
 RETURN NEW;
END;
$function$;
CREATE TRIGGER provenance_resolutions_validate BEFORE INSERT OR UPDATE ON provenance_resolutions
 FOR EACH ROW EXECUTE FUNCTION nerocd_validate_provenance_resolution();

CREATE OR REPLACE FUNCTION nerocd_provenance_schema_compatible() RETURNS boolean LANGUAGE sql STABLE AS $function$
 SELECT to_regclass(current_schema() || '.provenance_resolutions') IS NOT NULL
 AND EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='revisions' AND column_name='provenance_state' AND is_nullable='NO')
 AND EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid=to_regclass(current_schema() || '.provenance_resolutions') AND conname='provenance_resolutions_pkey')
 AND EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid=to_regclass(current_schema() || '.provenance_resolutions') AND conname='provenance_resolutions_deployment_id_attempt_fkey')
 AND to_regclass(current_schema() || '.revisions_resolved_service_content_identity_unique') IS NOT NULL
 AND to_regclass(current_schema() || '.provenance_resolutions_exact_authority') IS NOT NULL;
$function$;
