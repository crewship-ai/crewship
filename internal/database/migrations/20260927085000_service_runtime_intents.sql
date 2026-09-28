-- Opt-in durable service lifecycle. No rows are created for legacy on-demand services.
CREATE TABLE service_runtime_intents (
 id TEXT PRIMARY KEY,
 crew_id TEXT NOT NULL REFERENCES crews(id) ON DELETE CASCADE,
 service_name TEXT NOT NULL,
 desired_state TEXT NOT NULL CHECK(desired_state IN ('running','stopped')),
 version INTEGER NOT NULL DEFAULT 1 CHECK(version > 0),
 observed_state TEXT NOT NULL DEFAULT 'pending',
 last_error TEXT NOT NULL DEFAULT '',
 lease_owner TEXT NOT NULL DEFAULT '',
 lease_until TEXT NOT NULL DEFAULT '',
 next_attempt_at TEXT NOT NULL DEFAULT '',
 updated_at TEXT NOT NULL,
 UNIQUE(crew_id,service_name)
);
CREATE INDEX service_runtime_intents_due ON service_runtime_intents(next_attempt_at,lease_until);
