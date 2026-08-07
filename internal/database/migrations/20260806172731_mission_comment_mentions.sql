-- The resolved @mentions of one issue comment (#1768, item 3).
--
-- The wire format lives in the comment body — `[@label](crewship:agent/<id>)`
-- — and `internal/mentions` parses it out of the CommonMark AST. This table is
-- what the WRITE path leaves behind so no reader ever has to parse again.
--
-- Why a join table rather than a column on mission_comments:
--
--   * a mention is a relationship between a comment and an AGENT, and the
--     agent id belongs in a column with a foreign key to `agents` rather than
--     inside a JSON blob nothing can constrain. `ON DELETE CASCADE` then means
--     a deleted agent's mentions go with it instead of decaying into ids that
--     resolve to nothing;
--   * the reverse question — "what was this agent asked to do, and did it get
--     woken?" — is the one an operator asks when a mention did not fire, and a
--     column on the comment cannot be indexed for it;
--   * the set is RESOLVED, not parsed: an id that names no agent in the
--     comment's own workspace produces no row at all. A probe for a
--     foreign-workspace agent therefore leaves no trace here, which is what
--     makes "row exists" mean "this mention was real".
--
-- UNIQUE (comment_id, agent_id) is the de-duplication, at the schema level:
-- mentioning the same agent three times in one comment is one mention, one
-- activity row and one dispatch. ExtractAgentIDs already de-duplicates, but
-- the constraint is what makes that a property of the data rather than of the
-- caller remembering to.
--
-- `position` is the first-seen ordinal within the comment, so the set can be
-- replayed in the order it was written without re-parsing.
--
-- The dispatch columns record what the TRIGGER did, which is a separate fact
-- from what was mentioned:
--
--   dispatch_state = 'dispatched'  an assignment was created (assignment_id)
--                    'refused'     a delegation cap said no (dispatch_detail
--                                  carries the refusal, verbatim)
--                    'skipped'     nothing was attempted — no dispatcher is
--                                  wired on this instance, or the mention
--                                  named the comment's own author
--                    'failed'      the dispatch errored (dispatch_detail says)
--
-- Without these a mention that did not wake its agent is indistinguishable
-- from one that did, and the delegation caps' refusal — the whole point of
-- routing through the /assign chokepoint — would be invisible to the operator
-- who has to decide whether to raise the limit.
--
-- created_at uses the ISO T-form DEFAULT, not `datetime('now')`. v144
-- converted every legacy space-form DEFAULT in the schema precisely because
-- ' ' (0x20) sorts before 'T' (0x54), so a legacy-form row and an
-- RFC3339 row written by Go in the same column order wrongly. A new table
-- must not reintroduce the third shape.

CREATE TABLE IF NOT EXISTS mission_comment_mentions (
    id           TEXT NOT NULL PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id)       ON DELETE CASCADE,
    mission_id   TEXT NOT NULL REFERENCES missions(id)         ON DELETE CASCADE,
    comment_id   TEXT NOT NULL REFERENCES mission_comments(id) ON DELETE CASCADE,
    agent_id     TEXT NOT NULL REFERENCES agents(id)           ON DELETE CASCADE,

    position INTEGER NOT NULL DEFAULT 0,

    dispatch_state  TEXT NOT NULL DEFAULT 'skipped',
    assignment_id   TEXT REFERENCES assignments(id) ON DELETE SET NULL,
    dispatch_detail TEXT,

    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),

    UNIQUE (comment_id, agent_id)
);

-- "who was mentioned on this issue" — the issue card's read path.
CREATE INDEX IF NOT EXISTS idx_mission_comment_mentions_mission
    ON mission_comment_mentions(mission_id);

-- "what was this agent mentioned on" — the agent-side read, and the one an
-- operator uses when an agent claims it was never asked.
CREATE INDEX IF NOT EXISTS idx_mission_comment_mentions_agent
    ON mission_comment_mentions(agent_id);

-- Workspace scoping for the backup dump's generic filter.
CREATE INDEX IF NOT EXISTS idx_mission_comment_mentions_workspace
    ON mission_comment_mentions(workspace_id);
