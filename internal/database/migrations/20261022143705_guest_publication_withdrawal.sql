-- A withdrawn Guest publication keeps its immutable archive and manifest but
-- admits no new run and stops running ones; republishing clears it.
ALTER TABLE pipelines ADD COLUMN publication_withdrawn_at TEXT;

-- Hard limit for one Guest-published run, set by workspace OWNER/ADMIN.
ALTER TABLE workspaces ADD COLUMN guest_routine_max_run_minutes INTEGER NOT NULL DEFAULT 30
    CHECK(guest_routine_max_run_minutes BETWEEN 1 AND 240);
