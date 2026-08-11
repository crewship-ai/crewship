-- Index the foreign keys that a parent DELETE actually has to scan.
--
-- With `PRAGMA foreign_keys = ON`, deleting a parent row makes SQLite check
-- every child table that references it. When the child's referencing column is
-- not the leading column of an index, that check is a full table scan — and it
-- happens once per deleted parent row, while holding the single database-wide
-- write lock.
--
-- 48 foreign key columns in this schema have no leading index. This migration
-- adds 16 of them, and the 32 it leaves alone are the point of the exercise:
-- every index is paid for on every INSERT and UPDATE of the child, and the
-- migration immediately before this one (20260810120000) exists precisely
-- because eleven indexes had accumulated that nothing could ever use. Adding
-- all 48 would have traded one kind of waste for another.
--
-- Two conditions had to hold, and both were derived from the tree rather than
-- assumed:
--
--   1. THE PARENT IS ACTUALLY HARD-DELETED. Grepping `DELETE FROM <parent>`
--      across non-test Go finds: agents (3 sites), credentials (4), missions
--      (5), crews, chats, projects, milestones, checkpoints, workspaces
--      (restore path), and assignments (via a trigger).
--
--      `users` is NOT in that list — nothing in the tree hard-deletes a user
--      row. So the nine unindexed FK columns pointing at users
--      (attachments.uploaded_by_user_id, message_reactions.user_id,
--      peer_card_audit.actor_user_id, workspace_files.created_by, and the
--      rest) buy nothing today and are deliberately skipped. If user erasure
--      ever grows a working path, they become worth revisiting — and that
--      change should re-run the same check rather than trusting this comment.
--
--   2. THE CHILD TABLE GROWS. A scan of a settings table with one row per
--      workspace costs nothing; a scan of an append-only audit table costs
--      more every day. That is why keeper_governance_settings,
--      keeper_runtime_settings, keeper_aux_settings, composio_settings,
--      saved_views, triage_rules, recurring_issues, hooks_config, user_models
--      and pipelines are left unindexed even though their parents do get
--      deleted.
--
-- `DELETE FROM missions WHERE crew_id = ?` (crews_query.go) deserves its own
-- note: it removes many missions in one statement, and every one of them
-- re-checks each child table referencing missions. That is the worst shape in
-- the schema for an unindexed child, and it is why all three mission
-- references below are included even though eval_runs is small today.
--
-- All single-column, all named after the column they cover. None of them is a
-- prefix of a longer index on the same table —
-- TestSchemaHasNoRedundantIndexes fails the build if that ever stops being
-- true, so this migration cannot quietly reintroduce what the previous one
-- removed.

-- ── children of agents ────────────────────────────────────────────────
-- credential_audit is the fastest-growing table in the schema and has no
-- retention sweep; an agent deletion scanning it is the clearest case here.
CREATE INDEX IF NOT EXISTS idx_credential_audit_agent
    ON credential_audit(agent_id);
CREATE INDEX IF NOT EXISTS idx_attachments_uploaded_by_agent
    ON attachments(uploaded_by_agent_id);
CREATE INDEX IF NOT EXISTS idx_peer_card_audit_agent
    ON peer_card_audit(agent_id);
CREATE INDEX IF NOT EXISTS idx_port_exposures_agent
    ON port_exposures(agent_id);

-- ── children of crews ─────────────────────────────────────────────────
CREATE INDEX IF NOT EXISTS idx_port_exposures_crew
    ON port_exposures(crew_id);
CREATE INDEX IF NOT EXISTS idx_approvals_queue_crew
    ON approvals_queue(crew_id);
CREATE INDEX IF NOT EXISTS idx_checkpoints_crew
    ON checkpoints(crew_id);
CREATE INDEX IF NOT EXISTS idx_memory_health_snapshots_crew
    ON memory_health_snapshots(crew_id);

-- ── children of chats ─────────────────────────────────────────────────
CREATE INDEX IF NOT EXISTS idx_port_exposures_chat
    ON port_exposures(chat_id);
CREATE INDEX IF NOT EXISTS idx_message_feedback_chat
    ON message_feedback(chat_id);

-- ── children of missions ──────────────────────────────────────────────
-- See the note above on the bulk crew-scoped mission delete.
CREATE INDEX IF NOT EXISTS idx_approvals_queue_mission
    ON approvals_queue(mission_id);
CREATE INDEX IF NOT EXISTS idx_eval_runs_baseline_mission
    ON eval_runs(baseline_mission_id);
CREATE INDEX IF NOT EXISTS idx_eval_runs_candidate_mission
    ON eval_runs(candidate_mission_id);

-- ── children of credentials ───────────────────────────────────────────
CREATE INDEX IF NOT EXISTS idx_mission_code_links_credential
    ON mission_code_links(credential_id);

-- ── children of assignments ───────────────────────────────────────────
-- Deleted by trg_* when a chat goes; mentions grow with issue traffic.
CREATE INDEX IF NOT EXISTS idx_mission_comment_mentions_assignment
    ON mission_comment_mentions(assignment_id);

-- ── children of checkpoints (self-referential) ────────────────────────
CREATE INDEX IF NOT EXISTS idx_checkpoints_fork_of
    ON checkpoints(fork_of);
