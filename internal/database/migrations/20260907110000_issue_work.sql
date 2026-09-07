-- A human owner and the current worker are independent. Holding work for a
-- human fences every assignment writer, including agent tools and recovery.
CREATE TABLE issue_work (
    mission_id TEXT NOT NULL PRIMARY KEY REFERENCES missions(id) ON DELETE CASCADE,
    mode TEXT NOT NULL DEFAULT 'agent' CHECK(mode IN ('agent','human')),
    worker_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
    revision INTEGER NOT NULL DEFAULT 0,
    note TEXT NOT NULL DEFAULT '',
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
INSERT INTO issue_work(mission_id,mode,worker_user_id)
SELECT id, CASE WHEN assignee_type='user' AND delegate_agent_id IS NULL THEN 'human' ELSE 'agent' END,
CASE WHEN assignee_type='user' AND delegate_agent_id IS NULL THEN owner_user_id END
FROM missions WHERE mission_type='issue';
CREATE TRIGGER issue_work_create AFTER INSERT ON missions WHEN NEW.mission_type='issue'
BEGIN
 INSERT INTO issue_work(mission_id,mode,worker_user_id)
 VALUES(NEW.id, CASE WHEN NEW.assignee_type='user' AND NEW.delegate_agent_id IS NULL THEN 'human' ELSE 'agent' END,
 CASE WHEN NEW.assignee_type='user' AND NEW.delegate_agent_id IS NULL THEN NEW.owner_user_id END);
END;
CREATE UNIQUE INDEX issue_work_operation_id ON mission_activity(mission_id,source_id)
WHERE source_kind='work_operation';
CREATE TRIGGER issue_work_fence_insert BEFORE INSERT ON assignments
WHEN NEW.status NOT IN ('COMPLETED','FAILED','CANCELLED')
AND NOT EXISTS (SELECT 1 FROM assignments WHERE id=NEW.id)
AND EXISTS (
 SELECT 1 FROM issue_work w WHERE w.mode='human' AND
 (w.mission_id=NEW.mission_id OR w.mission_id=NEW.chat_id OR w.mission_id=NEW.group_id)
)
BEGIN SELECT RAISE(ABORT,'issue work is held by a human'); END;
CREATE TRIGGER issue_work_fence_start BEFORE UPDATE OF status ON assignments
WHEN NEW.status='RUNNING' AND EXISTS (
 SELECT 1 FROM issue_work w WHERE w.mode='human' AND
 (w.mission_id=NEW.mission_id OR w.mission_id=NEW.chat_id OR w.mission_id=NEW.group_id)
)
BEGIN SELECT RAISE(ABORT,'issue work is held by a human'); END;

-- Legacy create handlers fill typed assignment columns after inserting the row.
CREATE TRIGGER issue_work_initial_human AFTER UPDATE OF owner_user_id,assignee_id ON missions
WHEN NEW.assignee_type='user' AND NEW.delegate_agent_id IS NULL
BEGIN
 UPDATE issue_work SET mode='human',worker_user_id=NEW.owner_user_id
 WHERE mission_id=NEW.id AND revision=0;
END;

ALTER TABLE issue_work ADD COLUMN brief_revision INTEGER NOT NULL DEFAULT 0;
ALTER TABLE assignments ADD COLUMN issue_brief_revision INTEGER;
CREATE TRIGGER issue_work_brief_changed AFTER UPDATE OF description,title ON missions
WHEN NEW.description IS NOT OLD.description OR NEW.title IS NOT OLD.title
BEGIN
 UPDATE issue_work SET brief_revision=brief_revision+1,revision=revision+1 WHERE mission_id=NEW.id;
END;
CREATE TRIGGER issue_work_assignment_brief AFTER INSERT ON assignments
WHEN NEW.issue_brief_revision IS NULL
BEGIN
 UPDATE assignments SET issue_brief_revision=(SELECT brief_revision FROM issue_work WHERE mission_id=NEW.mission_id)
 WHERE id=NEW.id;
END;

-- A human reply resumes the waiting plan step, rather than launching an
-- unrelated comment run whose result can never unblock the original plan.
CREATE TRIGGER issue_work_resume_input AFTER INSERT ON assignments
WHEN NEW.created_by_user_id IS NOT NULL AND NEW.mission_id IS NOT NULL
BEGIN
 UPDATE mission_tasks SET assignment_id=NEW.id,status='IN_PROGRESS',completed_at=NULL
 WHERE mission_id=NEW.mission_id AND assigned_agent_id=NEW.assigned_to_id
 AND status='AWAITING_APPROVAL'
 AND assignment_id IN (SELECT id FROM assignments WHERE outcome='NEEDS_HUMAN' AND chat_id=NEW.chat_id);
END;

CREATE INDEX issue_work_worker_user ON issue_work(worker_user_id);
CREATE INDEX issue_comments_cursor ON mission_comments(mission_id,created_at,id);

-- The cursor index covers the old mission-only lookup as well.
DROP INDEX idx_mission_comments_mission;

-- A reply may resume one waiting step, never several tasks sharing a chat.
-- Ambiguous legacy plans must be clarified before any new assignment lands.
CREATE TRIGGER issue_work_reply_unambiguous BEFORE INSERT ON assignments
WHEN NEW.created_by_user_id IS NOT NULL AND NEW.mission_id IS NOT NULL
AND (SELECT COUNT(*) FROM mission_tasks t JOIN assignments a ON a.id=t.assignment_id
 WHERE t.mission_id=NEW.mission_id AND t.assigned_agent_id=NEW.assigned_to_id
 AND t.status='AWAITING_APPROVAL' AND a.outcome='NEEDS_HUMAN' AND a.chat_id=NEW.chat_id)>1
BEGIN SELECT RAISE(ABORT,'reply matches multiple waiting tasks; clarify the work handoff'); END;
