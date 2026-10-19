-- Rotating a trigger's signing secret can leave the old one valid for a while
-- (ODCH-INT-11), so a sender can be switched over without a window of 401s.
-- Both columns hold the PREVIOUS secret only: encrypted at rest like
-- signing_secret, and ignored once the expiry has passed. Never returned by any
-- read endpoint.
ALTER TABLE pipeline_webhooks ADD COLUMN previous_signing_secret TEXT;
ALTER TABLE pipeline_webhooks ADD COLUMN previous_secret_expires_at TEXT;
