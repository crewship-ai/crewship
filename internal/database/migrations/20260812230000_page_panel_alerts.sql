-- page_panel_alerts — the edge, remembered (docs/prd/pages.md §4 rule 4, §5).
--
-- Two things in Pages have to happen ONCE per event rather than once per
-- observation of it:
--
--   * an SLA lapse. Nothing sweeps panels today, so a sweeper is what notices;
--     a sweeper runs every minute and would open a duplicate issue every
--     minute for as long as the panel stays quiet.
--   * a wake gate firing. The condition that woke somebody usually persists
--     across the next push, and every push re-evaluates the gate.
--
-- A row here means "an alert of this kind is currently open for this panel".
-- Its absence means the panel is clear. That is the whole state machine, and
-- it is in the database rather than in the sweeper's memory for the reason the
-- push floor is a WHERE NOT EXISTS rather than a read-then-write: a restart, a
-- second replica or a missed tick must not be able to produce a second issue,
-- and only the database can arbitrate that.
--
-- WHY A ROW IS DELETED ON RECOVERY RATHER THAN MARKED CLEARED.
-- The history of what fired and when is the JOURNAL's job — page.panel.stale
-- and page.wake.fired are written on every transition, and they are what a
-- reader queries. Keeping tombstones here as well would be a second, weaker
-- copy of that record, and one that grows without a retention rule.
--
-- WHY THERE IS NO workspace_id.
-- panel_id resolves to page_panels → pages → workspace_id, and the alert is
-- meaningless without the panel. A denormalised copy would be a second answer
-- to "whose panel is this", and §7.1's whole point is that there is one.
CREATE TABLE IF NOT EXISTS page_panel_alerts (
    panel_id   TEXT NOT NULL REFERENCES page_panels(id) ON DELETE CASCADE,
    -- What is open: 'sla' for the freshness lapse an on_failure block routes,
    -- or 'wake:<n>' for the nth wake gate on the panel. Free text rather than
    -- a CHECK because the gate ordinal is part of it; the writers are two
    -- functions in one package.
    gate_key   TEXT NOT NULL,
    -- The issue this alert opened. Nullable and ON DELETE SET NULL: deleting
    -- the issue must not silently re-arm the alert (that would open a second
    -- issue on the next tick for a lapse a human has already closed by hand),
    -- and it must not fail either.
    issue_id   TEXT REFERENCES missions(id) ON DELETE SET NULL,
    opened_at  TEXT NOT NULL,
    -- Which crew the issue was opened on, kept so the journal entry written on
    -- recovery can name it without a join through a row that may be gone.
    crew_id    TEXT,
    PRIMARY KEY (panel_id, gate_key)
);

-- issue_id is a foreign-key child column and is read on every mission delete,
-- so it leads an index — the blanket rule the Pages migration states and this
-- one follows. panel_id needs none: it is the leading column of the primary
-- key, which SQLite indexes.
CREATE INDEX IF NOT EXISTS idx_page_panel_alerts_issue ON page_panel_alerts(issue_id);
