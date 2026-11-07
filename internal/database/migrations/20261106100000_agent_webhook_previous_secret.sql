-- Rotating an agent's webhook secret can leave the old one valid for a bounded
-- overlap window, so a sender can be switched over without a window of 401s.
-- Both columns hold the PREVIOUS secret only: encrypted at rest like
-- webhook_secret, ignored once the expiry has passed, and never returned by any
-- read endpoint. NULL means no overlap is open.
ALTER TABLE agents ADD COLUMN previous_webhook_secret TEXT;
ALTER TABLE agents ADD COLUMN previous_webhook_secret_expires_at TEXT;
