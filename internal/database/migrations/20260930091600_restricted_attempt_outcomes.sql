-- Content-free outcomes survive execution revocation. Text, provider errors,
-- credentials and runtime logs are intentionally outside this projection.
CREATE TABLE access_attempt_outcomes (
 attempt_id TEXT PRIMARY KEY REFERENCES access_attempts(id) ON DELETE CASCADE,
 state TEXT NOT NULL CHECK(state IN ('completed','failed','canceled','denied')),
 recorded_at TEXT NOT NULL
);
CREATE TRIGGER access_outcome_immutable BEFORE UPDATE ON access_attempt_outcomes BEGIN
 SELECT RAISE(ABORT,'restricted outcome is immutable');
END;
