-- #2274: scope inbox_items' dedupe key to the workspace.
--
-- idx_inbox_items_kind_source (v85) enforced UNIQUE(kind, source_id)
-- across the WHOLE INSTANCE. source_id is an opaque handle into whatever
-- produced the item — a waitpoint token, an escalation id, a run id — so
-- the writers that dedupe on it (internal/inbox upsertRow,
-- groupchatnotify) rely on the index both for the ON CONFLICT target and
-- for "one open item per source".
--
-- The instance-wide scope is what makes a forked restore
-- (--as-workspace / --as-crew) silently lose every inbox_items row: the
-- fork's copy carries the same (kind, source_id) as the source row that
-- is still sitting on the same instance, INSERT OR IGNORE eats it, and
-- the workspace's inbox arrives empty (#2274, the #2260 shape). Every
-- source_id is in practice globally unique anyway (they are CUIDs or
-- tokens), so widening the key with workspace_id removes no real
-- dedupe guarantee — it only stops two workspaces from fighting over
-- one namespace.
--
-- The upserts' ON CONFLICT targets are updated to
-- (workspace_id, kind, source_id) in the same change; SQLite requires a
-- conflict target to match a unique index exactly, so shipping one
-- without the other breaks at the first inbox write.

DROP INDEX IF EXISTS idx_inbox_items_kind_source;
CREATE UNIQUE INDEX idx_inbox_items_workspace_kind_source
    ON inbox_items (workspace_id, kind, source_id);
