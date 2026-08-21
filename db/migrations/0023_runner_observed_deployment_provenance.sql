-- Existing rows are historical, not runner observed.  Preserve them as
-- readable legacy_unverified records and never promote them automatically.
ALTER TABLE revisions ADD COLUMN provenance_state TEXT NOT NULL DEFAULT 'legacy_unverified';
ALTER TABLE revisions ADD COLUMN provenance_resolved BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE revisions ADD COLUMN resolved_at TIMESTAMPTZ;
ALTER TABLE revisions ALTER COLUMN git_commit SET DEFAULT '';
ALTER TABLE revisions ALTER COLUMN compose_hash SET DEFAULT '';
ALTER TABLE revisions ALTER COLUMN content_identity SET DEFAULT '';
ALTER TABLE revisions DROP CONSTRAINT IF EXISTS revisions_git_commit_check;
ALTER TABLE revisions DROP CONSTRAINT IF EXISTS revisions_compose_hash_check;
ALTER TABLE revisions DROP CONSTRAINT IF EXISTS revisions_content_identity_check;
-- The original service/content_identity uniqueness was correct only while a
-- revision was fully known at creation time.  Pending rows deliberately have
-- an empty canonical identity, so retain content de-duplication only once the
-- runner has resolved immutable evidence.  Historical rows remain readable.
ALTER TABLE revisions DROP CONSTRAINT IF EXISTS revisions_service_id_content_identity_key;
CREATE UNIQUE INDEX revisions_resolved_service_content_identity_unique
  ON revisions(service_id, content_identity) WHERE provenance_state='resolved';

CREATE OR REPLACE FUNCTION nerocd_sha256_digests(items TEXT[]) RETURNS BOOLEAN LANGUAGE sql IMMUTABLE AS $function$
 SELECT cardinality(items)>0
   AND NOT EXISTS (SELECT 1 FROM unnest(items) d WHERE d IS NULL OR d !~ '^sha256:[0-9a-f]{64}$');
$function$;
ALTER TABLE revisions ADD CONSTRAINT revisions_provenance_state CHECK (provenance_state IN ('pending','resolved','legacy_unverified'));
ALTER TABLE revisions ADD CONSTRAINT revisions_provenance_shape CHECK ((provenance_state='pending' AND NOT provenance_resolved AND resolved_at IS NULL AND git_commit='' AND compose_hash='' AND content_identity='' AND cardinality(image_digests)=0) OR (provenance_state='resolved' AND provenance_resolved AND resolved_at IS NOT NULL AND git_commit ~ '^[0-9a-f]{40,64}$' AND compose_hash ~ '^sha256:[0-9a-f]{64}$' AND content_identity=git_commit || ':' || compose_hash AND nerocd_sha256_digests(image_digests)) OR provenance_state='legacy_unverified');
CREATE OR REPLACE FUNCTION nerocd_revision_provenance_immutable() RETURNS trigger LANGUAGE plpgsql AS $function$
BEGIN
 IF OLD.provenance_state='resolved' AND NEW IS DISTINCT FROM OLD THEN
   RAISE EXCEPTION 'resolved revision provenance is immutable';
 END IF;
 IF OLD.provenance_state='legacy_unverified' AND NEW.provenance_state <> OLD.provenance_state THEN RAISE EXCEPTION 'legacy provenance cannot be promoted'; END IF;
 RETURN NEW;
END;
$function$;
CREATE TRIGGER revisions_provenance_immutable BEFORE UPDATE ON revisions FOR EACH ROW EXECUTE FUNCTION nerocd_revision_provenance_immutable();
