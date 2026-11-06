-- A curator can lock a skill against autonomous rewrites (#5045). While
-- locked, no agent, sidecar, consolidation or promotion path may change its
-- content and the lifecycle sweep may not move it to stale or archived.
-- Unlocked (0) is the default, so every existing skill behaves as before.
ALTER TABLE skills ADD COLUMN locked INTEGER NOT NULL DEFAULT 0;
ALTER TABLE skills ADD COLUMN locked_by TEXT;
ALTER TABLE skills ADD COLUMN locked_at TEXT;
ALTER TABLE skills ADD COLUMN locked_workspace_id TEXT;
