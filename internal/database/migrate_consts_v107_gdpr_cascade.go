package database

// migrationGDPRCascade (v107) lands the schema the F6 GDPR admin
// cascade endpoints (DELETE /api/v1/admin/users/{userId}/data,
// GET /api/v1/admin/users/{userId}/data) need to enumerate and
// purge everything we hold about a single data subject.
//
// The flagship problem this fixes is the EU-release blocker the
// auditor surfaced: today an operator honouring an Art. 17 erasure
// request has to walk peer_cards by user_id, but memory_versions
// (the EU AI Act Art. 14 audit ledger) and inbox_items (review
// proposals carrying user-attributable context) have no per-user
// pointer at all. A SAR delete that only touches peer_cards leaves
// the persona / memory ledger and the unresolved-inbox surface
// holding personal data — that's the violation we close here.
//
// # Per-table choice rationale
//
//   - peer_cards already carries user_id (v105). The composite
//     index idx_peer_cards_user_ws covers (user_id, workspace_id);
//     no new column needed. The admin DELETE handler joins through
//     this existing shape.
//
//   - memory_versions (v91) gets a NEW nullable data_subject_id
//     column. Persona writes and peer-tier writes that capture
//     content ABOUT a specific user must populate it so the cascade
//     can find every row. Legacy rows (pre-v107 + any tier where
//     no subject is attributable, e.g. crew / workspace tier) keep
//     it NULL — the cascade simply skips those, which is correct:
//     a workspace-scoped memory snapshot is not personal data.
//
//   - inbox_items (v85) gets a NEW nullable data_subject_id column.
//     Distinct from target_user_id (the operator who must act on
//     the item): persona suggestion items, peer card review items,
//     and similar HITL proposals carry content about a third
//     party. data_subject_id captures THAT identity so the SAR
//     cascade can purge the proposal even when the operator-to-
//     act-on-it is someone else entirely.
//
//   - keeper_requests is intentionally NOT extended. Its rows are
//     agent + crew + credential scoped (see v9 and v102) and carry
//     no user-attributable content. A SAR cascade has nothing to
//     do here. Documented for posterity — a future addition would
//     need its own migration once the table actually grows a
//     user-pointing column.
//
// # Audit table: gdpr_actions
//
// Every admin invocation of the cascade endpoints — both DELETE
// (Art. 17 erasure) and GET (Art. 15 access) — writes one row
// recording who acted on whom, when, what the scope ended up
// being, and the operator-supplied reason. The table is the
// accountability trail the auditor required: without it, "we
// deleted the user's data" has no defensible artefact.
//
// Idempotency: running DELETE twice for the same user writes TWO
// gdpr_actions rows (the second's scope_json simply shows the
// already-purged tables returned zero matches). The audit trail
// thus captures both attempts, which is the right shape — auditors
// want to see "did the operator try this twice and what happened
// each time" not "we deduplicated the call".
//
// scope_json is free-form JSON with the per-table counts the
// handler observed at apply time — e.g. {"peer_cards":3,
// "memory_versions":12,"inbox_items":1}. Keeping it open-ended
// rather than column-per-table avoids a schema change every time
// we add a new cascadable table.
//
// status / completed_at / error capture the synchronous handler's
// final outcome. The handler runs the cascade inline (not in a
// background worker) so 'in_progress' should be vanishingly rare
// in practice — it exists for the case where the request gets
// cancelled mid-cascade and the row is never updated. A future
// reconcile job could sweep rows stuck at 'in_progress' older
// than 1h to 'failed', but that's punted to a followup.
//
// reason is operator-supplied free text (e.g. "GDPR SAR ticket
// #1234"). Required on DELETE so the compliance trail names the
// triggering ticket; optional on GET (access requests are read-
// only and have weaker audit requirements).
const migrationGDPRCascade = `
-- memory_versions: data subject pointer for persona/peer writes
-- that capture content ABOUT a specific user. Legacy rows + tier=
-- workspace|crew|pins|learned rows keep NULL — the cascade skips
-- those, which is correct (non-personal data).
ALTER TABLE memory_versions ADD COLUMN data_subject_id TEXT;

CREATE INDEX IF NOT EXISTS idx_memory_versions_subject_ws
    ON memory_versions (data_subject_id, workspace_id)
    WHERE data_subject_id IS NOT NULL;

-- inbox_items: data subject pointer for HITL proposals that carry
-- content about a third party (e.g. persona suggestion, peer-card
-- review). Distinct from target_user_id (the operator who must
-- act). Legacy rows + ops-only items (failed_run, waitpoint) keep
-- NULL.
ALTER TABLE inbox_items ADD COLUMN data_subject_id TEXT;

CREATE INDEX IF NOT EXISTS idx_inbox_items_subject_ws
    ON inbox_items (data_subject_id, workspace_id)
    WHERE data_subject_id IS NOT NULL;

-- gdpr_actions: append-only audit of every admin SAR action.
-- One row per invocation of DELETE or GET /api/v1/admin/users/
-- {userId}/data, regardless of outcome. Indexed for the common
-- compliance queries: "show me everything we did about user X"
-- (subject) and "show me all SAR activity in the last 90 days"
-- (initiated_at).
CREATE TABLE IF NOT EXISTS gdpr_actions (
    id              TEXT PRIMARY KEY,
    workspace_id    TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    data_subject_id TEXT NOT NULL,
    actor_user_id   TEXT NOT NULL,
    action          TEXT NOT NULL CHECK (action IN ('export','delete','view')),
    scope_json      TEXT,
    initiated_at    TEXT NOT NULL DEFAULT (datetime('now','subsec')),
    completed_at    TEXT,
    status          TEXT NOT NULL DEFAULT 'in_progress'
                       CHECK (status IN ('in_progress','completed','failed')),
    error           TEXT,
    reason          TEXT,
    -- Belt-and-suspenders for the GDPR audit contract: API path
    -- already rejects a delete without a reason at the handler
    -- layer, but the column-level CHECK keeps the invariant alive
    -- even if a future caller bypasses the handler (admin SQL,
    -- another internal-auth route, partial revert). 'export' and
    -- 'view' actions don't require a reason — operators frequently
    -- run them as routine SAR servicing — only 'delete' needs the
    -- justification on file.
    CHECK (action <> 'delete' OR (reason IS NOT NULL AND reason <> ''))
);

CREATE INDEX IF NOT EXISTS idx_gdpr_actions_subject
    ON gdpr_actions (workspace_id, data_subject_id);

CREATE INDEX IF NOT EXISTS idx_gdpr_actions_initiated
    ON gdpr_actions (initiated_at);
`
