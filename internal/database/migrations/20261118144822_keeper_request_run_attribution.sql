-- Historical and legacy non-run calls remain unknown; never backfill boot runs.
ALTER TABLE keeper_requests ADD COLUMN requesting_run_id TEXT;
ALTER TABLE keeper_request_events ADD COLUMN requesting_run_id TEXT;
CREATE INDEX idx_keeper_requests_run ON keeper_requests(requesting_run_id, requesting_agent_id);
CREATE INDEX idx_keeper_request_events_run ON keeper_request_events(requesting_run_id, requesting_agent_id);

CREATE TRIGGER keeper_request_run_immutable
BEFORE UPDATE OF requesting_run_id ON keeper_requests
WHEN NEW.requesting_run_id IS NOT OLD.requesting_run_id
BEGIN
    SELECT RAISE(ABORT, 'keeper requesting run attribution is immutable');
END;
