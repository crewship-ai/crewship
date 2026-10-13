-- Existing and newly created placements remain fixed unless explicitly changed.
-- source_revision remains the retained baseline, including in follow_latest mode.
ALTER TABLE page_board_artifact_placements ADD COLUMN mode TEXT NOT NULL DEFAULT 'fixed' CHECK(mode IN ('fixed','follow_latest'));
