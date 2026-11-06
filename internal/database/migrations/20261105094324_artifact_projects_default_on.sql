-- Artifact Projects become a supported default (#4994). 20261027175100 seeded
-- the artifact_projects flag off; this turns it on, but only where the row is
-- still exactly that untouched seed, so no deliberate choice is overwritten:
--   * id 'ff_artifact_projects' — a flag an admin deleted and recreated
--     through the API has a random id and is left alone, and a deleted flag
--     stays deleted (this never inserts);
--   * updated_at = created_at — the seed wrote both with the same
--     datetime('now'); every admin change through PATCH
--     /api/v1/feature-flags/{key} (CLI "crewship feature-flag update",
--     manifest apply) rewrites updated_at in RFC 3339, so a flag an admin ever
--     touched, including turning it off, keeps its value.
-- Workspace overrides (feature_flag_overrides) are not touched: an explicit
-- workspace off still wins over the instance default. Re-running the
-- statement is a no-op.
UPDATE feature_flags SET enabled=1
 WHERE id='ff_artifact_projects' AND key='artifact_projects' AND enabled=0 AND updated_at=created_at;
