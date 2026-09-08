-- Review history can grow independently of the number of live issues.
CREATE INDEX issue_execution_reviewer ON issue_executions(reviewer_agent_id);
CREATE INDEX issue_execution_review_task ON issue_executions(review_task_id);
