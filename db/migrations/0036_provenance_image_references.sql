-- Provenance now retains the complete immutable image reference needed for a
-- runner to pull and inspect the artifact. Existing bare-digest evidence is
-- historical and remains readable; NOT VALID constraints apply the new shape
-- to every newly written or updated row without fabricating a repository.
CREATE OR REPLACE FUNCTION nerocd_image_references(items TEXT[]) RETURNS BOOLEAN LANGUAGE sql IMMUTABLE AS $function$
 SELECT cardinality(items)>0
   AND NOT EXISTS (
     SELECT 1
     FROM unnest(items) d
     WHERE d IS NULL
       OR d !~ '^[a-z0-9][a-z0-9._/:-]*@sha256:[a-f0-9]{64}$'
       OR (
         strpos(reverse(split_part(d, '@', 1)), ':') > 0
         AND (
           strpos(reverse(split_part(d, '@', 1)), '/') = 0
           OR strpos(reverse(split_part(d, '@', 1)), ':') < strpos(reverse(split_part(d, '@', 1)), '/')
         )
       )
   );
$function$;

ALTER TABLE revisions DROP CONSTRAINT IF EXISTS revisions_provenance_shape;
ALTER TABLE revisions ADD CONSTRAINT revisions_provenance_shape CHECK (
  (provenance_state='pending' AND NOT provenance_resolved AND resolved_at IS NULL AND git_commit='' AND compose_hash='' AND content_identity='' AND cardinality(image_digests)=0)
  OR (provenance_state='resolved' AND provenance_resolved AND resolved_at IS NOT NULL AND git_commit ~ '^[0-9a-f]{40,64}$' AND compose_hash ~ '^sha256:[0-9a-f]{64}$' AND content_identity=git_commit || ':' || compose_hash AND nerocd_image_references(image_digests))
  OR provenance_state='legacy_unverified'
) NOT VALID;

DO $block$
DECLARE constraint_name TEXT;
BEGIN
  FOR constraint_name IN
    SELECT conname
    FROM pg_constraint
    WHERE conrelid = 'provenance_resolutions'::regclass
      AND contype = 'c'
      AND pg_get_constraintdef(oid) LIKE '%nerocd_sha256_digests(image_digests)%'
  LOOP
    EXECUTE format('ALTER TABLE provenance_resolutions DROP CONSTRAINT %I', constraint_name);
  END LOOP;
END;
$block$;

ALTER TABLE provenance_resolutions ADD CONSTRAINT provenance_resolutions_image_references_check
  CHECK (nerocd_image_references(image_digests)) NOT VALID;
