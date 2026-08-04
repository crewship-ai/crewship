-- Link-first Git integration: an issue can carry one or more external code
-- links (a GitHub pull request or a GitLab merge request today).
--
-- Issues ARE missions with an `identifier` (see v33-v41), so this table sits
-- beside mission_relations / mission_comments / mission_activity and uses the
-- same `mission_` prefix. Deletes are hard, not soft, for the same reason
-- mission_relations' are: a code link is an assertion someone made about an
-- issue, and un-asserting it leaves nothing worth auditing behind. (The
-- neighbours carry no `deleted_at`; adding one only here would make the
-- issue-subresource family inconsistent for no reader's benefit.)
--
-- Shape notes, in the order they will matter:
--
--   provider/host/owner/repo/number is the PARSED identity of the link, kept
--   in columns rather than only in `url` so phase 2 does not need a migration
--   rewrite. The two things phase 2 wants — "when a merge lands, which issue
--   does it belong to?" and "has anyone already linked this PR?" — are both
--   lookups BY that tuple, not by issue, which is why idx_mission_code_links_ref
--   exists below despite nothing reading it yet today. A webhook payload
--   carries exactly (host, owner, repo, number); it does not carry our URL
--   string, so an index on `url` would not answer it.
--
--   `kind` is 'pull_request' for every row this migration's code can write.
--   It exists so a later 'commit' / 'branch' link is a new value, not a new
--   table.
--
--   The remote_* columns are the provider's timestamps, kept distinct from
--   created_at/updated_at (ours). remote_merged_at is what phase 2's
--   auto-transition will trigger on; it is populated from today.
--
--   credential_id records WHICH stored credential fetched this link, so a
--   revoked key's blast radius is a query rather than an investigation. ON
--   DELETE SET NULL: losing the credential must not delete the link.
--
--   last_sync_error holds the last refresh failure verbatim (provider name,
--   status, reason). A link whose token was revoked stays visible and says
--   why, instead of silently freezing on stale state.

CREATE TABLE IF NOT EXISTS mission_code_links (
    id           TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    mission_id   TEXT NOT NULL REFERENCES missions(id) ON DELETE CASCADE,

    -- Parsed identity of the link.
    provider TEXT    NOT NULL,                             -- 'GITHUB' | 'GITLAB'
    host     TEXT    NOT NULL,                             -- 'github.com', 'gitlab.acme.internal:8443'
    owner    TEXT    NOT NULL,                             -- GitHub owner, or a GitLab group/subgroup path
    repo     TEXT    NOT NULL,
    number   INTEGER NOT NULL,                             -- PR number / MR iid
    kind     TEXT    NOT NULL DEFAULT 'pull_request',
    url      TEXT    NOT NULL,                             -- canonical web URL rebuilt from the parse

    -- State fetched from the provider. NULL until the first successful fetch.
    title         TEXT,
    state         TEXT,                                    -- 'OPEN'|'DRAFT'|'MERGED'|'CLOSED'|'UNKNOWN'
    author        TEXT,
    source_branch TEXT,
    target_branch TEXT,

    remote_created_at TEXT,
    remote_updated_at TEXT,
    remote_merged_at  TEXT,
    remote_closed_at  TEXT,

    credential_id   TEXT REFERENCES credentials(id) ON DELETE SET NULL,
    last_synced_at  TEXT,
    last_sync_error TEXT,

    -- Who attached it. Exactly one of the two is set: a human through the
    -- public API/CLI, or an agent through the sidecar. Mirrors missions'
    -- author_agent_id / created_by_user_id pairing (v129).
    created_by_user_id  TEXT REFERENCES users(id),
    created_by_agent_id TEXT REFERENCES agents(id),

    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now')),

    -- Attaching the same PR to the same issue twice is a duplicate, not a
    -- second link. Scoped to the parsed tuple rather than `url` so
    -- .../pull/7 and .../pull/7/files collapse to one row.
    UNIQUE (mission_id, provider, host, owner, repo, number)
);

CREATE INDEX IF NOT EXISTS idx_mission_code_links_mission
    ON mission_code_links(mission_id);
CREATE INDEX IF NOT EXISTS idx_mission_code_links_workspace
    ON mission_code_links(workspace_id);

-- Reverse lookup: "which issues does this PR belong to?" See the note above —
-- this is the phase-2 (merge → status transition) access path, and building it
-- now is what keeps that change additive.
CREATE INDEX IF NOT EXISTS idx_mission_code_links_ref
    ON mission_code_links(provider, host, owner, repo, number);
