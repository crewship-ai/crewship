package database

// migrationChannelAgents (v170) adds the agent↔channel pairing table that
// gates agent-initiated notifications.
//
// An agent can now send to a notification channel itself (the notify_send
// tool inside the container). The question that answers is not "can it reach
// the network" — it always could — but "which of this workspace's channels is
// THIS agent allowed to post to", and the answer has to default to none.
//
// Default-deny is the whole point. A workspace channel is something an admin
// stood up for the team; an agent that could post to any of them by virtue of
// existing would turn one confused or prompt-injected agent into a
// workspace-wide megaphone, on a surface (Slack, Discord) where people cannot
// tell an agent's message from a colleague's at a glance. So a pairing is an
// explicit, auditable row a human creates, per (channel, agent).
//
// Modelled as a join table rather than a JSON column on notification_channels
// because the natural queries run both ways — "what may this agent post to"
// on every send, and "who can post here" when an admin reviews a channel —
// and a JSON array serves neither with an index.
//
// ON DELETE CASCADE on both sides: deleting a channel or an agent must not
// leave a pairing that would silently re-authorise a recycled id.
const migrationChannelAgents = `
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
`
