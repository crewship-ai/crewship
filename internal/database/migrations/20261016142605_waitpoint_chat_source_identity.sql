-- A chat card must never expose the public callback bearer capability.
-- Identity metadata lives on the existing source, not in another answer store.
ALTER TABLE pipeline_waitpoints ADD COLUMN chat_source_id TEXT;
UPDATE pipeline_waitpoints SET chat_source_id='wpc_' || lower(hex(randomblob(16)));
CREATE UNIQUE INDEX idx_pipeline_waitpoints_chat_source_id ON pipeline_waitpoints(chat_source_id);

-- Cover the canonical creator and legacy INSERT producers without broad changes
-- to callback or Inbox token semantics. Restores preserve supplied source IDs.
CREATE TRIGGER pipeline_waitpoints_chat_source_identity_insert
AFTER INSERT ON pipeline_waitpoints
WHEN NEW.chat_source_id IS NULL OR NEW.chat_source_id=''
BEGIN
  UPDATE pipeline_waitpoints SET chat_source_id='wpc_' || lower(hex(randomblob(16))) WHERE token=NEW.token;
END;

CREATE TRIGGER pipeline_waitpoints_chat_source_identity_immutable
BEFORE UPDATE OF chat_source_id ON pipeline_waitpoints
WHEN OLD.chat_source_id IS NOT NULL AND OLD.chat_source_id<>'' AND NEW.chat_source_id IS NOT OLD.chat_source_id
BEGIN
  SELECT RAISE(ABORT, 'waitpoint chat source identity is immutable');
END;
