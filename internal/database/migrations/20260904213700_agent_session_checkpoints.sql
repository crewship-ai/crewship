-- agent_session_checkpoints (PRD-ISSUES-AND-ROUTINES-2026 §9.5, work package
-- B5, #2345).
--
-- The structured hand-off HANDOFF already proves out per task
-- (internal/orchestrator/mission.go:100-137, enforced at mission_tasks.go:
-- 321-329) but cannot serve here: mission_tasks.handoff_context is ONE
-- overwritten column, per TASK, with no sequence marker — §9.5's "keep all
-- rows" requirement (an agent resuming a session needs its OWN most recent
-- checkpoint, and losing every earlier one the moment a new one lands would
-- make "what did I already do" answerable only for the last run) rules it
-- out. Nothing else on the schema has this shape either — the closest
-- candidate, issue_agent_sessions (20260904095702), is deliberately ONE row
-- per (mission, agent): a durable cursor, not a history of documents.
--
-- One JSON column, not four (done/plan/facts/blockers/next_step/confidence)
-- — this codebase's own convention for a small structured document
-- (mission_activity.payload_json, approvals_queue.payload,
-- pipeline_waitpoints.decision_payload). checkpoint_json is written by
-- internal/api's session-checkpoint write path (issue_checkpoints.go),
-- parsed from a ---CHECKPOINT--- block the way HANDOFF is parsed
-- (orchestrator.ParseCheckpoint, mirroring orchestrator.ParseHandoff) —
-- Parsed=false is recorded explicitly, in the JSON body, when the model does
-- not comply (§11.3, the same "measurable rather than invisible" precedent
-- mission_tasks_completion.go:98 sets for HANDOFF).
--
-- Explicitly NOT in journal_entries (§9.5, I8, F36): the journal's 30-day
-- compaction sweep would delete exactly the row that lets a resumed agent
-- skip already-finished work, and the whole point of this table is that it
-- OUTLIVES that sweep. Retention is this table's own (§16.1): checkpoints
-- are excluded from the journal compaction sweep by construction — they are
-- not in the journal in the first place — and are pruned, if ever, by a
-- future policy scoped to this table alone.
--
-- ── Cascades (F55) ──────────────────────────────────────────────────────
--
-- workspace_id: CASCADE — this table's own copy of the rule (I7), never
-- relying on the mission chain.
-- session_id: CASCADE to issue_agent_sessions — a checkpoint is meaningless
-- once the session that wrote it is gone (agent hard-deleted, mission
-- hard-deleted; issue_agent_sessions itself cascades from both).
-- run_id: deliberately NO FK, matching issue_agent_sessions.active_run_id's
-- own reasoning (20260904095702's comment): assignments rows are
-- delete-and-recreate-shaped in tests and can be pruned independently of
-- the checkpoint that names them. A dangling run_id costs a lookup miss on
-- "which run wrote this", not a leak — the checkpoint itself, keyed on
-- session_id, is what continuity actually reads.
CREATE TABLE IF NOT EXISTS agent_session_checkpoints (
    id               TEXT NOT NULL PRIMARY KEY,
    workspace_id     TEXT NOT NULL REFERENCES workspaces(id)          ON DELETE CASCADE,
    session_id       TEXT NOT NULL REFERENCES issue_agent_sessions(id) ON DELETE CASCADE,
    run_id           TEXT,
    seq_at_write     INTEGER NOT NULL DEFAULT 0,
    checkpoint_json  TEXT NOT NULL,
    created_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

-- "the latest checkpoint for this session" (§11.1 item 3) — the read every
-- context-pack assembly does. created_at DESC (not seq_at_write DESC): two
-- checkpoints can share a seq_at_write when nothing else was written to the
-- mission's event log between two runs on the same session, and insertion
-- order is the tie-breaker that actually matches "most recent".
CREATE INDEX IF NOT EXISTS idx_agent_session_checkpoints_session
    ON agent_session_checkpoints(session_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_agent_session_checkpoints_workspace
    ON agent_session_checkpoints(workspace_id);

-- Consistency trigger, copied from issue_agent_sessions' own shape
-- (20260904095702) rather than trusting the two FKs above to agree with
-- each other on workspace: a session and a workspace can both exist and
-- both be real while disagreeing about which workspace the session
-- belongs to (they never should, but nothing about a bare FK reference
-- catches that — see §16.1's tenancy rule, applied identically here).
CREATE TRIGGER IF NOT EXISTS trg_agent_session_checkpoints_consistency_ins
BEFORE INSERT ON agent_session_checkpoints
BEGIN
    SELECT RAISE(ABORT, 'checkpoint session must belong to the checkpoint workspace')
    WHERE (SELECT workspace_id FROM issue_agent_sessions WHERE id = NEW.session_id) IS NOT NEW.workspace_id;
END;
