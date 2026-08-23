-- Onboarding proposal store (docs/prd/conversational-onboarding.md §5.6, §8.2).
--
-- The setup agent that will eventually drive onboarding chat gets NO write
-- permission at all — it produces a proposal, and only the human's click on
-- Create applies it, under the human's own session. This table is where that
-- proposal lives between the two.
--
-- The load-bearing property, copied from `internal/manifest` Plan/Apply
-- (§5.6): the human-readable card and the mutation it will execute come from
-- the SAME struct, captured at the SAME moment — propose time. There is no
-- second path that re-derives what to write after the human clicks Create.
-- payload_json IS that struct, serialized. POST .../apply reads ONLY this
-- column to decide what to write; it never re-reads the mutable template or
-- reads proposal content from its own request body, which carries nothing
-- but the id in the path.
--
-- Phase 1 scope (§7): "template + model swap, nothing more" — the payload
-- names one crew_templates row (template_slug) and at most one model
-- override, and the agent list is the fully-resolved roster that template +
-- override combination will produce (name, slug, role_title, llm_provider,
-- llm_model, system_prompt per agent) — not raw agent-authored text. Storing
-- the resolved roster, rather than just the inputs, is what lets the
-- integrity test (§8.2) assert the applied crew equals the card FIELD FOR
-- FIELD: apply writes this resolved snapshot directly.
--
-- status starts 'PENDING' and moves to 'APPLIED' exactly once — the CHECK
-- ties the terminal fields together so a row can never claim to be applied
-- without recording what it applied to (applied_crew_id) and what the
-- create returned (applied_result_json, the deployCrewResult snapshot).
-- Re-applying an already-APPLIED proposal returns that stored snapshot
-- instead of calling deployCrewTemplate again — the idempotency the task
-- requires, and the reason the snapshot is kept rather than re-derived from
-- the crews/agents tables at read time (those rows could drift or be
-- deleted independently of the proposal's own history).
CREATE TABLE IF NOT EXISTS onboarding_proposals (
    id                  TEXT PRIMARY KEY,
    workspace_id        TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    created_by          TEXT NOT NULL REFERENCES users(id),
    created_at          TEXT NOT NULL DEFAULT (datetime('now','subsec')),
    applied_at          TEXT,
    status              TEXT NOT NULL DEFAULT 'PENDING'
                          CHECK (status IN ('PENDING', 'APPLIED')),
    payload_json        TEXT NOT NULL,
    applied_crew_id     TEXT REFERENCES crews(id),
    applied_result_json TEXT,
    CHECK (
      (status = 'PENDING' AND applied_at IS NULL AND applied_crew_id IS NULL AND applied_result_json IS NULL) OR
      (status = 'APPLIED' AND applied_at IS NOT NULL AND applied_crew_id IS NOT NULL AND applied_result_json IS NOT NULL)
    )
);

CREATE INDEX IF NOT EXISTS idx_onboarding_proposals_workspace_created
    ON onboarding_proposals (workspace_id, created_at DESC);

-- crews ARE hard-deleted (internal/api/internal_status.go issues
-- `DELETE FROM crews WHERE id = ? AND workspace_id = ?`), so
-- applied_crew_id is a hot foreign key by the same rule
-- migration 20260810154153 applied elsewhere: leave it unindexed and every
-- crew delete full-scans onboarding_proposals to enforce the reference.
-- created_by -> users is NOT indexed here on purpose: nothing in the tree
-- hard-deletes a users row, so that index would be pure write cost (see
-- migrate_index_hot_foreign_keys_test.go's userForeignKeysStayUnindexed).
CREATE INDEX IF NOT EXISTS idx_onboarding_proposals_applied_crew_id
    ON onboarding_proposals (applied_crew_id);
