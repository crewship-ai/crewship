-- Native save attempts that reconcile proved did not commit (#4899). The
-- reconcile writes the row under the document writer fence; a save carrying
-- that attempt id checks it inside its own publication transaction, so a save
-- still pending after validation can never commit once its client was told
-- it did not. Rows bind to the lease holder session and generation that sent
-- the attempt; only that live holder can retire, rows whose lease is no longer
-- live are pruned on every retirement, and a page holds at most 8 rows
-- (internal/mooring/native_save_receipt.go retireAttempt).
CREATE TABLE craft_native_retired_attempts (
 page_id TEXT NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 session_id TEXT NOT NULL,
 attempt_id TEXT NOT NULL,
 generation INTEGER NOT NULL CHECK(generation>0),
 base_version INTEGER NOT NULL CHECK(base_version>0),
 PRIMARY KEY(page_id,session_id,attempt_id)
);
CREATE INDEX craft_native_retired_attempts_workspace ON craft_native_retired_attempts(workspace_id);
