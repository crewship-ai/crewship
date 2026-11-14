-- Who created or last saved a routine schedule (#5339). A scheduled routine
-- run may reach its agent's standing artifacts only when a person created or
-- last saved the schedule; that person's standing is re-checked at every run.
-- creator_kind is 'human' (with that person's user id) or 'agent' (an agent
-- or any internal path saved it; never a user id, whatever header it sent).
-- Existing schedules keep NULL: their creator is unknown, so they reach no
-- artifact until a person saves them again. Forward-only.
ALTER TABLE pipeline_schedules ADD COLUMN creator_kind TEXT CHECK (creator_kind IS NULL OR creator_kind IN ('human','agent'));
ALTER TABLE pipeline_schedules ADD COLUMN creator_user_id TEXT CHECK (
  (creator_kind IS NULL AND creator_user_id IS NULL)
  OR (creator_kind = 'agent' AND creator_user_id IS NULL)
  OR (creator_kind = 'human' AND creator_user_id IS NOT NULL AND creator_user_id <> '')
);

-- Crew deletion cascades into artifact_unattended_runs (#5339); index the
-- foreign key so that cascade does not scan the table.
CREATE INDEX IF NOT EXISTS artifact_unattended_runs_crew ON artifact_unattended_runs(crew_id);
