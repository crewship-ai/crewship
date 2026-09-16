-- Index the user FK independently of workspace-scoped provenance reads.
CREATE INDEX idx_user_model_provenance_user_id ON user_model_provenance (user_id);
