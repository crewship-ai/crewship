package database

// v133: outbound notification channels (issue #850). Workspace-scoped
// delivery targets — e-mail or signed webhook — that a run's terminal
// path fans out to when a routine completes or fails, so the news
// reaches someone who isn't looking at the in-product inbox.
//
// config_json carries the non-secret config: {"url": "..."} for a
// webhook, {"to": "..."} for e-mail. secret_enc holds the webhook HMAC
// signing secret encrypted at rest (internal/encryption, same scheme as
// credentials); NULL for e-mail channels. events_json is the JSON array
// of run-terminal event types the channel wants — defaults to
// ["run.failed"] so a routine that runs hourly doesn't flood an inbox
// with success pings; opt into completions with run.completed. Soft-
// deleted via deleted_at so an audit of "who could this workspace have
// notified" survives removal.
const migrationNotificationChannels = `
CREATE TABLE notification_channels (
    id            TEXT PRIMARY KEY,
    workspace_id  TEXT NOT NULL,
    type          TEXT NOT NULL CHECK (type IN ('email','webhook')),
    config_json   TEXT NOT NULL DEFAULT '{}',
    secret_enc    TEXT,
    events_json   TEXT NOT NULL DEFAULT '["run.failed"]',
    enabled       INTEGER NOT NULL DEFAULT 1,
    created_by    TEXT,
    created_at    TEXT NOT NULL DEFAULT (datetime('now','subsec')),
    updated_at    TEXT NOT NULL DEFAULT (datetime('now','subsec')),
    deleted_at    TEXT
);
CREATE INDEX idx_notification_channels_ws
    ON notification_channels (workspace_id) WHERE deleted_at IS NULL;
`
