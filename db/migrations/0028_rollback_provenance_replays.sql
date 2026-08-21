-- A resolved revision may be deployed again by a fenced retry or rollback
-- child. Each attempt needs its own immutable acknowledgement, so the
-- original one-resolution-per-revision constraint is too restrictive.
ALTER TABLE provenance_resolutions
  DROP CONSTRAINT IF EXISTS provenance_resolutions_revision_id_key;

