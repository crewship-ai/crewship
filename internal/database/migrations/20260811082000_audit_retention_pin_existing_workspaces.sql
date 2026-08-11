-- Pin existing workspaces to "keep forever" for credential_audit.
--
-- Companion to 20260810170000_audit_retention_windows, which added the column.
--
-- Without this line, upgrading is destructive: the sweeper performs one
-- immediate sweep at boot, every existing workspace resolves NULL to the
-- 90-day default, and a year of credential access history is DELETEd before
-- the operator has any chance to set the override — the API that sets it only
-- comes up after the sweeper has already run. No dry-run, no grace period,
-- and nothing to recover from short of a pre-upgrade backup.
--
-- A retention window is a decision about data somebody may be required to
-- keep. Shipping a default is fine; applying it retroactively to history that
-- accumulated under "we never delete this" is not, and it is the same
-- reasoning that makes audit_logs unlimited by default.
--
-- So the asymmetry is deliberate: a workspace that existed before this
-- migration keeps everything until its operator says otherwise, while a
-- workspace created afterwards leaves the column NULL and gets the 90-day
-- product default. New installs are bounded; existing installs are asked.
--
-- On a fresh database `workspaces` is empty, so this matches no rows and the
-- default applies to everything created later — which is the intent.
UPDATE workspaces
   SET credential_audit_retention_days = 0
 WHERE credential_audit_retention_days IS NULL;
