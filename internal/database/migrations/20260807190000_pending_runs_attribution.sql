-- pending_runs carried no trigger attribution, so PendingRunDispatcher had
-- nothing to honour and hard-coded every deferred run as
-- `triggered_via='schedule', triggered_by_id=<pending row id>`.
--
-- Survivable while the only producer was the deferred-run endpoint. Not
-- survivable once automations enqueue here: every rule-fired run told the
-- operator a cron did it, and the rule's identity survived only as a shape
-- inside metadata_json that each reader had to reverse-engineer.
--
-- Nullable with no default on purpose. An existing row means "I did not say",
-- which is a different fact from "I said schedule", and the dispatcher keeps
-- applying its documented default to those. A DEFAULT 'schedule' here would
-- rewrite what every historical row claims.
ALTER TABLE pending_runs ADD COLUMN triggered_via TEXT;
ALTER TABLE pending_runs ADD COLUMN triggered_by_id TEXT;
