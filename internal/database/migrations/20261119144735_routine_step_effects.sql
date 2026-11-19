-- One row per routine `crewship` step write that committed (#5332). The route
-- inserts it in the SAME transaction as its write, so a step resumed after a
-- crash finds it and is answered from it instead of writing again. The
-- response is the step output the executor would have persisted; it lives and
-- dies with its run (and workspace), like pipeline_run_step_outputs.
CREATE TABLE routine_step_effects (
 run_id TEXT NOT NULL REFERENCES pipeline_runs(id) ON DELETE CASCADE,
 operation_key TEXT NOT NULL CHECK(length(operation_key) BETWEEN 1 AND 200),
 workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 verb TEXT NOT NULL,
 request_hash TEXT NOT NULL,
 status_code INTEGER NOT NULL,
 response TEXT NOT NULL CHECK(json_valid(response)),
 created_at TEXT NOT NULL,
 PRIMARY KEY(run_id, operation_key)
);
CREATE INDEX idx_routine_step_effects_workspace ON routine_step_effects(workspace_id);
