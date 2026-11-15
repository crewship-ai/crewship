-- Agent run token on the sidecar page save (#5336). With agent access modes
-- on, a save without a run token is accepted under the crew hourly limit
-- only, unless the workspace opts in to require the token here. Default off:
-- old sidecars in running containers keep saving until an admin turns it on.
ALTER TABLE workspace_artifact_creation_policy
 ADD COLUMN save_requires_run_token INTEGER NOT NULL DEFAULT 0
 CHECK(save_requires_run_token IN (0,1));
