-- The attention contract's server-side columns (PRD-ISSUES-AND-ROUTINES-2026
-- §12, work package B10, #2364): `thread_key`, `attention_class` and
-- `actions_json` on inbox_items.
--
-- Today the §12 action contract (attention_class, thread_key, actions,
-- who_can_act, context) lives ONLY inside payload_json, written ad hoc by
-- whichever producer happens to include it (B6's run_needs_human is the one
-- example so far — see internal/api/issue_outcome_inbox.go). Nothing can
-- query or merge on it. That is F28's root cause as much as the client-side
-- fetch pattern is: without a queryable thread_key, the server has no way to
-- recognise that two rows describe the same recurring condition, so the
-- client did the merging instead (payload.approval_id/inbox_item_id
-- cross-links, a paginated missions walk for context).
--
-- Promoting thread_key/attention_class/actions to real columns lets
-- internal/inbox.WriteThreaded (writer.go) merge in place: a second producer
-- writing under the SAME (workspace_id, thread_key) updates the existing
-- open row instead of raising a sibling card. This is the fix for the
-- live-observed duplicate on dev1 (#2364's comment): one `routine save
-- --draft` raised both the B8 receipt ("Routine trigger ready", pinning
-- routine_version) and the older governance card ("Routine proposed for
-- review") because they had no shared identity beyond "the same routine" —
-- now they share thread_key "routine:<workspace>:<slug>" and collapse to one
-- row (internal/api/pipeline_governance.go, internal/api/pipeline_trigger.go).
--
-- attention_class is the closed §12 vocabulary (decision|input|review|
-- repair); left NULLable because most existing rows (pre-B10 kinds this PR
-- does not touch) never set one, and a CHECK admitting NULL keeps them
-- valid. actions_json defaults to '[]' (never NULL) so every reader can
-- json_decode it unconditionally, the same convention payload_json already
-- uses ('{}' default).
--
-- SQLite has no ALTER TABLE ... ADD COLUMN with a CHECK referencing the
-- column itself in older releases reliably across engines this project
-- targets, and the table already needs the standard inbox_items rebuild
-- pattern (v90, v162, v168, 20260728110000, 20260901180845,
-- 20260904233805) for the new CHECK — so this migration follows that exact
-- pattern rather than three separate ADD COLUMNs. The kind CHECK list is
-- UNCHANGED (B10 adds no new inbox kind: the digest scheduler, see
-- internal/inbox/digest.go, reuses kind='message' with
-- payload.subkind='digest', the same discriminator convention
-- routine_update already established for progress notices).
--
-- inbox_item_reads.inbox_item_id REFERENCES inbox_items(id) ON DELETE
-- CASCADE (added 20260902071500). Per 20260904233805's documented finding,
-- DROP TABLE inbox_items cascades against inbox_item_reads even though the
-- same ids reappear moments later under the renamed table — so the per-user
-- read markers must be saved to a TEMP table before the drop and restored
-- after the rename, exactly as that migration does.
CREATE TEMP TABLE _inbox_item_reads_preserved_b10 AS SELECT * FROM inbox_item_reads;

CREATE TABLE inbox_items_new (
    id                  TEXT PRIMARY KEY,
    workspace_id        TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    kind                TEXT NOT NULL
                          CHECK (kind IN ('waitpoint', 'escalation', 'failed_run', 'message',
                                          'memory_consolidation', 'schedule_missed',
                                          'schedule_circuit_breaker_tripped',
                                          'webhook_fire_failed', 'automation_enqueue_failed',
                                          'run_needs_human')),
    source_id           TEXT NOT NULL,
    target_user_id      TEXT,
    target_role         TEXT,
    title               TEXT NOT NULL,
    body_md             TEXT,
    sender_type         TEXT,
    sender_id           TEXT,
    sender_name         TEXT,
    state               TEXT NOT NULL DEFAULT 'unread'
                          CHECK (state IN ('unread', 'read', 'resolved')),
    priority            TEXT NOT NULL DEFAULT 'medium'
                          CHECK (priority IN ('urgent', 'high', 'medium', 'low')),
    blocking            INTEGER NOT NULL DEFAULT 1,
    payload_json        TEXT NOT NULL DEFAULT '{}',
    -- §12 attention contract, promoted from payload to real columns so the
    -- server can query and merge on them instead of the client.
    thread_key          TEXT,
    attention_class     TEXT CHECK (attention_class IS NULL OR
                                     attention_class IN ('decision', 'input', 'review', 'repair')),
    actions_json        TEXT NOT NULL DEFAULT '[]',
    read_at             TEXT,
    read_by_user_id     TEXT,
    resolved_at         TEXT,
    resolved_by_user_id TEXT,
    resolved_action     TEXT,
    created_at          TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at          TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    data_subject_id     TEXT
);

INSERT INTO inbox_items_new (
    id, workspace_id, kind, source_id, target_user_id, target_role,
    title, body_md, sender_type, sender_id, sender_name,
    state, priority, blocking, payload_json,
    read_at, read_by_user_id, resolved_at, resolved_by_user_id, resolved_action,
    created_at, updated_at, data_subject_id
)
SELECT
    id, workspace_id, kind, source_id, target_user_id, target_role,
    title, body_md, sender_type, sender_id, sender_name,
    state, priority, blocking, payload_json,
    read_at, read_by_user_id, resolved_at, resolved_by_user_id, resolved_action,
    created_at, updated_at, data_subject_id
FROM inbox_items;

DROP TABLE inbox_items;
ALTER TABLE inbox_items_new RENAME TO inbox_items;

CREATE INDEX IF NOT EXISTS idx_inbox_items_workspace_state_created
    ON inbox_items (workspace_id, state, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_inbox_items_unread
    ON inbox_items (workspace_id)
    WHERE state = 'unread';

CREATE UNIQUE INDEX IF NOT EXISTS idx_inbox_items_kind_source
    ON inbox_items (kind, source_id);

CREATE INDEX IF NOT EXISTS idx_inbox_items_subject_ws
    ON inbox_items (data_subject_id, workspace_id)
    WHERE data_subject_id IS NOT NULL;

-- The lookup internal/inbox.WriteThreaded runs on every threaded write: find
-- the open row (if any) sharing this workspace + thread_key. Partial (only
-- rows that carry a thread_key at all) and does not need `state` in the
-- predicate — an open-vs-resolved split would need a second index for the
-- resolved half, and resolved rows for a thread are rare enough that
-- WriteThreaded's own `state != 'resolved'` filter on top of this index is
-- cheap.
CREATE INDEX IF NOT EXISTS idx_inbox_items_thread
    ON inbox_items (workspace_id, thread_key)
    WHERE thread_key IS NOT NULL;

-- Restore the read markers the rebuild's cascade removed, now that
-- inbox_items exists again with the same ids (20260904233805's pattern).
INSERT INTO inbox_item_reads (inbox_item_id, user_id, read_at)
    SELECT inbox_item_id, user_id, read_at FROM _inbox_item_reads_preserved_b10;
DROP TABLE _inbox_item_reads_preserved_b10;
