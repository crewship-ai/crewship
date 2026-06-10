package database

// migrationPipelineRunsDefinitionHash (v114) adds definition_hash to
// pipeline_runs: sha256(definition_json) of the pipeline AS OF run
// start, stamped by the executor's persistRunStart.
//
// Why a new column instead of reusing pipeline_version (which v83
// added for exactly this kind of provenance): the version store
// dedupes pipeline_versions rows by content hash, so an in-place edit
// cycle A→B→A leaves pipelines.head_version pointing at B's row while
// the live definition is A — version numbers cannot be trusted as a
// drift signal. The content hash can.
//
// The boot-time resume scan (resume.go buildResumePlan) compares the
// stamped hash against the pipeline's current definition_hash and
// falls back to "interrupted (definition changed)" on mismatch. This
// closes the in-place-edit gap where every step id survives an edit
// and the step-id-existence gate alone would happily replay outputs
// produced by different step content.
//
// NULL on rows from before this migration — those keep the weaker
// step-id gate, which matches pre-v114 behaviour exactly.
const migrationPipelineRunsDefinitionHash = `
ALTER TABLE pipeline_runs ADD COLUMN definition_hash TEXT;
`
