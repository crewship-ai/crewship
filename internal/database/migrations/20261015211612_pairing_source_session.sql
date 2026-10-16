-- Legacy pending codes cannot prove their source session. They remain
-- unredeemable; users can request a fresh code from their browser session.
ALTER TABLE cli_pairings ADD COLUMN source_session_id TEXT;
