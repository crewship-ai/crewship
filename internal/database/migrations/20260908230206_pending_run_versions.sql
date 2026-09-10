-- NULL preserves the historical live-at-dispatch meaning for existing rows.
-- New one-time starts pin an archived version at acceptance; no silent backfill.
ALTER TABLE pending_runs ADD COLUMN pinned_version INTEGER CHECK (pinned_version IS NULL OR pinned_version > 0);
