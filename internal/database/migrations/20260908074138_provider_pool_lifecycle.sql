-- Definition revisions are independent of round-robin selection turns.
ALTER TABLE provider_login_pools ADD COLUMN revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0);
ALTER TABLE provider_login_pools ADD COLUMN deleted_at TEXT;
