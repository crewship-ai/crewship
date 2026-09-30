ALTER TABLE access_attempts ADD COLUMN completed_at TEXT;
CREATE TRIGGER access_attempt_completion_monotonic BEFORE UPDATE OF completed_at ON access_attempts
WHEN OLD.completed_at IS NOT NULL AND NEW.completed_at IS NOT OLD.completed_at
BEGIN
 SELECT RAISE(ABORT,'completed attempt cannot resume');
END;
