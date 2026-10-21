-- Pin authenticated recurring-template admission separately from authorship.
ALTER TABLE recurring_issues ADD COLUMN chat_origin_id TEXT REFERENCES execution_origins(id);
CREATE INDEX idx_recurring_issues_chat_origin ON recurring_issues(chat_origin_id) WHERE chat_origin_id IS NOT NULL;
