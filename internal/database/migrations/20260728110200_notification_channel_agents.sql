-- Agent to channel pairing for agent-initiated notifications.
--
-- Default-deny: an agent can post nowhere until a human grants it a specific
-- channel. The grant is the authorization, which is why an agent send is not
-- additionally filtered through the per-user preference matrix — letting
-- someone's mute silently swallow it would make an approved grant look broken
-- from both ends.
--
-- Authored as v170 and renumbered on merge; see the note in
-- 20260728110000_inbox_kinds.sql.

CREATE TABLE IF NOT EXISTS notification_channel_agents (
    id            TEXT PRIMARY KEY,
    workspace_id  TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    channel_id    TEXT NOT NULL REFERENCES notification_channels(id) ON DELETE CASCADE,
    agent_id      TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    -- The human who granted it, for the "who allowed this?" question an
    -- audit inevitably asks. Nullable so a seed or an import doesn't fail.
    granted_by    TEXT,
    created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    UNIQUE (channel_id, agent_id)
);

CREATE INDEX IF NOT EXISTS idx_notification_channel_agents_agent
    ON notification_channel_agents (agent_id);

CREATE INDEX IF NOT EXISTS idx_notification_channel_agents_channel
    ON notification_channel_agents (channel_id);
