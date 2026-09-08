-- Device-code sign-in to a model provider (docs/prd/provider-logins.md §5.6
-- v2, §10.3): the server, not a codex binary, drives OpenAI's device
-- authorization and turns the result into a credential.
--
-- One row per started sign-in. The row exists so a server restart does not
-- lose a code somebody is in the middle of typing into a browser: on boot
-- every pending row is picked up and polled again until it completes or its
-- expires_at passes. The pairing table (v86, cli_pairings) is the precedent —
-- same idea, other direction: there the code is ours and a CLI redeems it;
-- here the code is the provider's and a person redeems it in the provider's
-- browser page.
--
-- What is NOT here: any token. The provider's device_auth_id and the user
-- code are the handles of an unfinished flow, worthless once it ends; the
-- tokens the flow produces go straight into credentials through the same
-- create path POST /api/v1/credentials uses, and credential_id points at the
-- row. error_text is the operator-facing reason for a denied/expired row.
--
-- status is CHECK-constrained so a writer cannot invent a fifth state the
-- CLI's wait loop does not know how to leave.

CREATE TABLE IF NOT EXISTS provider_device_logins (
    id               TEXT PRIMARY KEY,
    workspace_id     TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    user_id          TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider         TEXT NOT NULL,                       -- 'OPENAI'
    mode             TEXT NOT NULL DEFAULT 'subscription', -- 'subscription' | 'api_key'
    device_auth_id   TEXT NOT NULL,                       -- the provider's handle for this flow
    user_code        TEXT NOT NULL,                       -- what the person types in the browser
    verification_url TEXT NOT NULL,
    interval_s       INTEGER NOT NULL DEFAULT 5,          -- poll interval the provider asked for
    status           TEXT NOT NULL DEFAULT 'pending'
                       CHECK (status IN ('pending', 'complete', 'expired', 'denied')),
    credential_id    TEXT,                                -- set when status = 'complete'
    error_text       TEXT,                                -- set when status = 'denied' or 'expired'
    created_at       TEXT NOT NULL,
    expires_at       TEXT NOT NULL,                       -- created_at + the provider's 15 min
    completed_at     TEXT,
    CHECK (
      (status = 'complete' AND credential_id IS NOT NULL AND completed_at IS NOT NULL) OR
      (status <> 'complete' AND credential_id IS NULL)
    )
);

-- Boot resume: every pending row, oldest first.
CREATE INDEX IF NOT EXISTS idx_provider_device_logins_status
    ON provider_device_logins (status, expires_at);

-- The status poll: the caller reads its own row by id, and the rate limit
-- counts a user's recent starts.
CREATE INDEX IF NOT EXISTS idx_provider_device_logins_user
    ON provider_device_logins (user_id, created_at);
