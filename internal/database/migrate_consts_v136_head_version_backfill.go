package database

// migrationHeadVersionBackfill (v136) reconciles pipelines.head_version
// with the version row whose content is actually live (#996).
//
// Before the #996 fix, a save whose content hash matched an existing
// pipeline_versions row (the A→B→A edit cycle) deduped WITHOUT
// repointing head_version — pipelines.definition_json became A while
// head_version stayed on B's row. New saves can no longer drift, but
// rows that drifted before the fix would keep lying to every
// head-derived surface (the versions API's is_head, CLI/UI HEAD
// markers) until their next save or rollback touched them.
//
// The UPDATE repoints head_version at the row matching the pipeline's
// live definition_hash. UNIQUE (pipeline_id, definition_hash) makes the
// subquery single-row by construction. Pipelines whose live hash has no
// version row (pre-v79 rows never re-saved) are left untouched — there
// is nothing truthful to point at, and inventing a row would falsify
// the history this fix exists to protect.
const migrationHeadVersionBackfill = `
UPDATE pipelines
SET head_version = (
    SELECT v.version FROM pipeline_versions v
    WHERE v.pipeline_id = pipelines.id
      AND v.definition_hash = pipelines.definition_hash
)
WHERE EXISTS (
    SELECT 1 FROM pipeline_versions v
    WHERE v.pipeline_id = pipelines.id
      AND v.definition_hash = pipelines.definition_hash
      AND v.version != pipelines.head_version
);
`
