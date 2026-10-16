-- Keep every receipt: old human requests do not contain a revision precondition.
-- This index supports temporary throughput admission without deleting retry evidence.
CREATE INDEX idx_document_receipts_window ON document_receipts(workspace_id,page_id,created_at);
