-- A review belongs to one execution of one revision, never to all historical runs.
ALTER TABLE issue_work ADD COLUMN client_review_required INTEGER NOT NULL DEFAULT 1 CHECK(client_review_required IN (0,1));
CREATE TABLE issue_executions (
 id TEXT PRIMARY KEY,
 mission_id TEXT NOT NULL REFERENCES missions(id) ON DELETE CASCADE,
 work_revision INTEGER NOT NULL,
 brief_revision INTEGER NOT NULL,
 attempt INTEGER NOT NULL DEFAULT 0,
 stage TEXT NOT NULL CHECK(stage IN ('working','reviewing','accepted','changes_requested','needs_human','failed','superseded')),
 reviewer_agent_id TEXT NOT NULL REFERENCES agents(id),
 review_task_id TEXT REFERENCES mission_tasks(id),
 verdict TEXT,
 review_note TEXT NOT NULL DEFAULT '',
 routine_run_id TEXT,
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL
);
CREATE UNIQUE INDEX issue_execution_active ON issue_executions(mission_id) WHERE stage IN ('working','reviewing');
CREATE INDEX issue_execution_history ON issue_executions(mission_id,created_at,id);
ALTER TABLE assignments ADD COLUMN issue_execution_id TEXT REFERENCES issue_executions(id);
ALTER TABLE mission_tasks ADD COLUMN issue_execution_id TEXT REFERENCES issue_executions(id);
CREATE INDEX issue_execution_assignments ON assignments(issue_execution_id,status);
CREATE INDEX issue_execution_tasks ON mission_tasks(issue_execution_id,status);
CREATE TRIGGER issue_execution_assignment AFTER INSERT ON assignments
WHEN NEW.mission_id IS NOT NULL AND NEW.issue_execution_id IS NULL
BEGIN
 UPDATE assignments SET issue_execution_id=(SELECT id FROM issue_executions
 WHERE mission_id=NEW.mission_id AND stage IN ('working','reviewing') LIMIT 1)
 WHERE id=NEW.id;
END;
CREATE TRIGGER issue_execution_handoff AFTER UPDATE OF revision,mode ON issue_work
WHEN NEW.revision<>OLD.revision OR NEW.mode<>OLD.mode
BEGIN
 UPDATE issue_executions SET stage='superseded',updated_at=NEW.updated_at
 WHERE mission_id=NEW.mission_id AND stage IN ('working','reviewing');
END;

ALTER TABLE issue_work ADD COLUMN submitted_brief_revision INTEGER;

CREATE TRIGGER issue_execution_stopped AFTER UPDATE OF status ON missions
WHEN OLD.status='IN_PROGRESS' AND NEW.status<>'IN_PROGRESS'
BEGIN
 UPDATE issue_executions SET stage=CASE WHEN NEW.status='FAILED' THEN 'failed' ELSE 'superseded' END,updated_at=NEW.updated_at
 WHERE mission_id=NEW.id AND stage IN ('working','reviewing');
END;
