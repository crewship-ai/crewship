-- Freeze the conversation authority used on admission. A lease renewal may
-- never switch to a restored audience or a recreated private data namespace.
ALTER TABLE access_attempts ADD COLUMN chat_generation TEXT NOT NULL DEFAULT '';
ALTER TABLE access_attempts ADD COLUMN chat_revision INTEGER NOT NULL DEFAULT 0;
UPDATE access_attempts SET
    chat_generation=(SELECT authority_generation FROM chats WHERE id=access_attempts.chat_id),
    chat_revision=(SELECT authority_revision FROM chats WHERE id=access_attempts.chat_id);
