CREATE TABLE chat_run_audits (
    chat_id TEXT NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
    run_id TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'incomplete' CHECK (status IN ('incomplete', 'archived', 'omitted')),
    wire BLOB,
    PRIMARY KEY (chat_id, run_id),
    CHECK (wire IS NULL OR (length(wire) > 0 AND length(wire) <= 16777216)),
    CHECK (status <> 'omitted' OR wire IS NULL)
);
