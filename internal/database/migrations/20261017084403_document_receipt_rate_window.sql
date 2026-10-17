-- Serve bounded receipt admission without dropping historical retry receipts.
CREATE INDEX idx_document_receipts_window ON document_receipts(workspace_id,page_id,created_at);
