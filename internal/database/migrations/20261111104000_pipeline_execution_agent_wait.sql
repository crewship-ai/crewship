-- NULL preserves unknown timing for legacy records and aggregate parent steps.
ALTER TABLE pipeline_step_executions ADD COLUMN agent_wait_ms INTEGER CHECK (agent_wait_ms IS NULL OR agent_wait_ms >= 0);
