-- The activation boundary and cursor are local to this installation. No
-- historical journal entries are replayed or scanned to seed the outbox.
CREATE TABLE run_failure_notice_state (
 id INTEGER PRIMARY KEY CHECK(id=1),
 enabled_at TEXT NOT NULL,
 watermark INTEGER NOT NULL DEFAULT 0
);
INSERT INTO run_failure_notice_state(id,enabled_at)
VALUES (1,strftime('%Y-%m-%dT%H:%M:%fZ','now'));

CREATE TABLE run_failure_notice_outbox (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 run_id TEXT NOT NULL UNIQUE REFERENCES work_attempts(run_id) ON DELETE CASCADE,
 projected INTEGER NOT NULL DEFAULT 0 CHECK(projected IN (0,1))
);

CREATE TRIGGER queue_run_failure_notice
AFTER UPDATE OF run_status ON work_attempts
WHEN NEW.run_status IN ('FAILED','TIMEOUT') AND OLD.run_status='' AND NEW.ended_at IS NOT NULL
BEGIN
 INSERT OR IGNORE INTO run_failure_notice_outbox(run_id)
 SELECT NEW.run_id
 FROM work_items w JOIN run_failure_notice_state s ON s.id=1
 WHERE w.id=NEW.work_id
  AND julianday(NEW.started_at)>=julianday(s.enabled_at)
  AND ((w.source='chat' AND w.domain_kind='host_chat_turn' AND w.authorized_scope='chat:send' AND w.authorized_by_user_id<>'')
    OR (w.source='assignment' AND w.domain_kind='assignment' AND w.authorized_scope='assignment:'||w.domain_id
      AND (w.authorized_by_user_id<>'' OR EXISTS(SELECT 1 FROM assignments a
       WHERE a.id=w.domain_id AND a.workspace_id=w.workspace_id AND a.chat_origin_id IS NOT NULL))));
END;
