ALTER TABLE conversation_messages ADD COLUMN run_id TEXT NOT NULL DEFAULT '';
CREATE INDEX conversation_messages_run_id ON conversation_messages(run_id) WHERE run_id <> '';
