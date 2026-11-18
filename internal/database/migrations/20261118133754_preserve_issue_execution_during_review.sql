-- Review is an active phase. Preserve the current execution while the issue
-- awaits its gate, and stop it on departure from either active status.
DROP TRIGGER issue_execution_stopped;
CREATE TRIGGER issue_execution_stopped AFTER UPDATE OF status ON missions
WHEN OLD.status IN ('IN_PROGRESS','REVIEW') AND NEW.status NOT IN ('IN_PROGRESS','REVIEW')
BEGIN
 UPDATE issue_executions SET stage=CASE WHEN NEW.status='FAILED' THEN 'failed' ELSE 'superseded' END,updated_at=NEW.updated_at
 WHERE mission_id=NEW.id AND stage IN ('working','reviewing');
END;
