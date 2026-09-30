-- Pending provider requests are already debited in cost_ledger. A crash does
-- not refund them; only validated terminal usage may reduce the debit.
CREATE TABLE restricted_cost_reservations (
    id TEXT PRIMARY KEY,
    ledger_id TEXT NOT NULL UNIQUE REFERENCES cost_ledger(id) ON DELETE RESTRICT,
    principal_id TEXT NOT NULL,
    attempt_id TEXT NOT NULL,
    credential_id TEXT NOT NULL,
    max_input_tokens INTEGER NOT NULL CHECK(max_input_tokens > 0),
    max_output_tokens INTEGER NOT NULL CHECK(max_output_tokens > 0),
    reserved_usd REAL NOT NULL CHECK(reserved_usd > 0),
    rate_input_per_m REAL NOT NULL CHECK(rate_input_per_m > 0),
    rate_output_per_m REAL NOT NULL CHECK(rate_output_per_m > 0),
    rate_cached_per_m REAL NOT NULL CHECK(rate_cached_per_m >= 0),
    state TEXT NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','known','unknown')),
    input_tokens INTEGER,
    output_tokens INTEGER,
    cached_input_tokens INTEGER
);
CREATE INDEX restricted_cost_attempt ON restricted_cost_reservations(attempt_id);
-- A reservation's immutable price/scope provenance cannot be repriced later.
CREATE TRIGGER restricted_cost_reservation_immutable
BEFORE UPDATE ON restricted_cost_reservations
WHEN NEW.id IS NOT OLD.id OR NEW.ledger_id IS NOT OLD.ledger_id
  OR NEW.principal_id IS NOT OLD.principal_id OR NEW.attempt_id IS NOT OLD.attempt_id
  OR NEW.credential_id IS NOT OLD.credential_id
  OR NEW.max_input_tokens IS NOT OLD.max_input_tokens OR NEW.max_output_tokens IS NOT OLD.max_output_tokens
  OR NEW.reserved_usd IS NOT OLD.reserved_usd OR NEW.rate_input_per_m IS NOT OLD.rate_input_per_m
  OR NEW.rate_output_per_m IS NOT OLD.rate_output_per_m OR NEW.rate_cached_per_m IS NOT OLD.rate_cached_per_m
BEGIN SELECT RAISE(ABORT, 'immutable restricted cost reservation'); END;
