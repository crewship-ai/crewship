package database

// migrationWebhookRequireTimestamp (v132) adds an opt-in per-agent policy that
// requires inbound webhooks to use the timestamped signature scheme.
//
// #789 added the Stripe/Svix timestamped scheme (X-Timestamp + HMAC over
// "<ts>.<body>", freshness-bounded) but kept it OPTIONAL: a captured body-only
// signed webhook stays replayable indefinitely, bounded only by the dedup
// window. #815 closes that class on demand — when webhook_require_timestamp is
// set, the handler rejects the two replayable shapes (body-only HMAC and the
// deprecated plaintext X-Webhook-Secret) with 400, forcing the timestamped
// scheme whose window bounds replay.
//
// Defaults to 0 (off) so existing senders don't break at upgrade; operators
// flip it per agent once their sender emits X-Timestamp. Additive and
// defaulted, so existing rows read back as "not required" — no backfill hook.
const migrationWebhookRequireTimestamp = `
ALTER TABLE agents ADD COLUMN webhook_require_timestamp INTEGER NOT NULL DEFAULT 0;
`
