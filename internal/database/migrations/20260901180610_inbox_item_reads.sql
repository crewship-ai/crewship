-- Per-user inbox read state (PRD-ISSUES-AND-ROUTINES-2026.md §9.7, WP A7).
--
-- ── The defect ──────────────────────────────────────────────────────────
--
-- inbox_items.read_at / read_by_user_id are single columns on a SHARED row.
-- The inboxVisibilityClause (internal/api/inbox_handler.go) makes
-- target_role a genuinely multi-recipient address — every MANAGER, say, can
-- see a MANAGER-targeted escalation — but the PATCH .../state=read handler
-- writes read_at/read_by_user_id with COALESCE(existing, now): the FIRST
-- user to open a role-targeted item marks it read for every other recipient
-- too. On a multi-user workspace that is a correctness bug, not a missing
-- feature: a second manager's inbox silently drops an item they never saw.
--
-- ── The fix ─────────────────────────────────────────────────────────────
--
-- inbox_item_reads is the per-(item, user) read marker. The API computes its
-- per-caller `state` as inbox_items.state (unchanged, still authoritative
-- for 'resolved' — that outcome IS shared: one decision closes the item for
-- everyone) LEFT JOINed against this table for the caller's own row to
-- decide 'read' vs 'unread'.
--
-- inbox_items.read_at / read_by_user_id are NOT dropped and NOT repurposed.
-- They keep answering the question they always answered — "has ANYONE dealt
-- with this" — which stays a real, separate, useful signal (e.g. "is this
-- escalation stale" for an operator dashboard that doesn't care which
-- manager looked). PATCH state=read keeps writing them exactly as before
-- (first-write-wins via COALESCE) in addition to upserting this table.
--
-- ── Shape ───────────────────────────────────────────────────────────────
--
-- Composite PK (inbox_item_id, user_id): a read marker's whole identity IS
-- the pair, there is no reason to widen it into an "id" one row can steal,
-- and PRIMARY KEY makes "mark read" an idempotent upsert
-- (INSERT ... ON CONFLICT DO UPDATE) the same way chat_read_cursors (v130)
-- already does for the identical shape ("has THIS user seen THIS thing").
--
-- No workspace_id column. Scoped transitively through inbox_item_id, which
-- is NOT NULL (workspaceFilterSQL's chat_read_cursors precedent: a filter on
-- a nullable hop silently drops rows, a NOT NULL hop is safe to special-case
-- in internal/backup/dbdump.go rather than carry a denormalised column).
--
-- ── ON DELETE ───────────────────────────────────────────────────────────
--
-- inbox_item_id ON DELETE CASCADE: a read marker for an item that no longer
-- exists (inbox_handler.go's Purge, or a source-managed row cleanup) means
-- nothing — it cannot orphan into a phantom unread/read badge because
-- nothing reads it once its parent is gone.
--
-- user_id ON DELETE CASCADE: matches every other per-user marker table in
-- this schema (chat_read_cursors, peer_cards, user_models — see
-- migrate_consts_v130_chat_unread.go). users(id) rows are never hard-deleted
-- by the GDPR erasure cascade (AdminGDPRHandler.DeleteUserData purges
-- CONTENT about a subject, not the account row), so this edge is a backstop
-- for the one remaining hard-delete path (account closure / instance
-- cleanup) rather than something the erasure endpoint itself relies on —
-- that endpoint gets its own explicit DELETE (§16.1's mandatory GDPR block)
-- because ON DELETE CASCADE only fires when the users row itself is
-- deleted, and a SAR erasure never deletes that row.
--
-- No separate data_subject_id column: user_id already IS the data subject
-- here — the row's entire content is "this user read this item at this
-- time", so there is no third party the row could be about that user_id
-- doesn't already name (contrast inbox_items.data_subject_id, which exists
-- precisely because target_user_id there is the person who must ACT, a
-- different identity from who the content is ABOUT).

CREATE TABLE IF NOT EXISTS inbox_item_reads (
    inbox_item_id TEXT NOT NULL REFERENCES inbox_items(id) ON DELETE CASCADE,
    user_id       TEXT NOT NULL REFERENCES users(id)       ON DELETE CASCADE,
    read_at       TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    PRIMARY KEY (inbox_item_id, user_id)
);

-- The reverse lookup: "which items has this user read" / the GDPR erasure
-- cascade's `WHERE user_id = ?`. The PK already covers inbox_item_id as its
-- leading column; user_id needs its own index to avoid a full scan.
CREATE INDEX IF NOT EXISTS idx_inbox_item_reads_user
    ON inbox_item_reads(user_id);

-- Backfill: every existing (read_at, read_by_user_id) pair becomes one row.
-- INSERT OR IGNORE because a rerun (or a row a concurrent write already
-- upserted between the ALTER and here) must not abort the migration on the
-- PK collision.
INSERT OR IGNORE INTO inbox_item_reads (inbox_item_id, user_id, read_at)
SELECT id, read_by_user_id, read_at
FROM inbox_items
WHERE read_by_user_id IS NOT NULL AND read_at IS NOT NULL;
