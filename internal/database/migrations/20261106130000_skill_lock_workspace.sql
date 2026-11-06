-- A skill lock belongs to the workspace that placed it (#5045). The skills
-- table is one catalog shared by every workspace, so the lock has to record its
-- holder. No foreign key: a lapsed holder is handled where the lock is changed.
ALTER TABLE skills ADD COLUMN locked_workspace_id TEXT;
