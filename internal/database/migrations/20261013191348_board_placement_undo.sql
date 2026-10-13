-- Tombstones retain opaque placement identity and independent source pointers;
-- restoring rechecks current Page ACLs and never restores a grant.
ALTER TABLE page_board_artifact_placements ADD COLUMN placement_rev INTEGER NOT NULL DEFAULT 1 CHECK(placement_rev>0);
ALTER TABLE page_board_artifact_placements ADD COLUMN deleted_at TEXT;
CREATE INDEX idx_board_placement_live ON page_board_artifact_placements(board_page_id,deleted_at);
ALTER TABLE page_board_mutations ADD COLUMN placement_revs_json TEXT NOT NULL DEFAULT '{}';
