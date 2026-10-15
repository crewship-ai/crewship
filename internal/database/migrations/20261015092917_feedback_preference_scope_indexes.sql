-- Agent retirement cascades must not scan every personal preference.
CREATE INDEX idx_agent_user_preferences_agent ON agent_user_preferences(agent_id);
-- Opt-out and account data cleanup select a person's preferences across agents;
-- the workspace-first subject index cannot serve that access pattern.
CREATE INDEX idx_agent_user_preferences_user ON agent_user_preferences(user_id,workspace_id);
