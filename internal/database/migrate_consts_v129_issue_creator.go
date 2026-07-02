package database

// migrationIssueCreatorAttribution (v129) adds creator-identity columns
// to `missions` so an issue (mission_type='issue') can say WHO created
// it — a human or an agent. v108 already added the provenance trio
// (author_chat_id, author_run_id, authored_via), but neither of the two
// identity halves existed:
//
//   - author_agent_id     — the agent that created the row via the
//     sidecar tool-call path (internal/api/issues_internal.go Create).
//     Mirrors pipelines.author_agent_id (v78) in name and semantics.
//   - created_by_user_id  — the authenticated user that created the row
//     via the public API (issue_handler_create.go). Mirrors
//     agents.created_by_user_id (v100).
//
// Exactly one of the two is set on new rows (agent path vs. human path);
// both stay NULL on legacy rows — the response layer omits the creator
// object rather than guessing. Plain TEXT (no REFERENCES) matches the
// v98 credentials attribution approach: attribution must survive the
// referenced actor being deleted, and must never block a GDPR cascade
// (v107).
//
// Net-new nullable columns on an existing table — no backfill, no
// SQLite recreate dance. Partial indices back "issues created by X"
// lookups without taxing the (majority) legacy rows.
const migrationIssueCreatorAttribution = `
ALTER TABLE missions ADD COLUMN author_agent_id TEXT;
ALTER TABLE missions ADD COLUMN created_by_user_id TEXT;

CREATE INDEX IF NOT EXISTS idx_mission_author_agent ON missions(author_agent_id)
    WHERE author_agent_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_mission_created_by_user ON missions(created_by_user_id)
    WHERE created_by_user_id IS NOT NULL;
`
