package database

// migrationAddProposalScoring (v91) adds the score_json TEXT column
// to memory_proposals so the consolidator can persist the six-signal
// scoring breakdown alongside each proposal (PR #4 step 2). The
// column is NOT NULL DEFAULT '{}' so the schema stays valid even
// for existing pre-v91 rows; new proposal writes populate it with
// the JSON shape consolidate.ScoreResult marshals to.
//
// Why a column rather than a sibling table:
//
//   - One row per proposal already in memory_proposals; one
//     scoring blob per row maps 1:1. A sibling table would force a
//     join on every explain query without any analytical benefit
//     (we don't query inside the JSON blob; SQLite's JSON1 ops are
//     for the explain endpoint to surface the fields, not for
//     filtering).
//   - SQLite's TEXT-NOT-NULL-DEFAULT pattern is the established
//     extension shape in this schema (evidence_json on the same
//     table predates this column).
//
// Schema impact: additive only — existing rows get '{}' so the
// explain endpoint's JSON-decode path can blindly trust the
// column is present and parseable.
const migrationAddProposalScoring = `
ALTER TABLE memory_proposals
    ADD COLUMN score_json TEXT NOT NULL DEFAULT '{}';
`
