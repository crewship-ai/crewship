-- assignments.exec_id / exec_container_id / hard_stop_at / hard_stop_result
-- (PRD-ISSUES-AND-ROUTINES-2026 §10.3 Tier 2, work package B7 — #2356).
--
-- Tier 1 (A1, merged) stamps cancel_requested_at and waits for the run to
-- notice at its next step boundary — there is no kill primitive, and the
-- crew's container is shared by every agent on the crew, so nothing here
-- may ever act container-wide. Tier 2 adds exactly one new capability: find
-- the OS pid behind the run's live exec and signal THAT pid, from a new
-- exec into the SAME container. That needs two things a row did not carry
-- before: which exec is live, and which container it is live in.
--
-- exec_id: ExecResult.ExecID (internal/provider/container.go), stamped by
-- the orchestrator's AgentRunRequest.OnExecStarted callback the moment the
-- agent's own exec is created (internal/orchestrator/orchestrator_run.go) —
-- before any output has streamed, so a hard stop requested in the first
-- second of a run still finds it. Overwritten on a retry so it always names
-- the CURRENT exec, never a stale one from an earlier attempt on the same
-- row.
--
-- exec_container_id: the container that exec_id lives in. Not re-derived
-- from crew_id at hard-stop time on purpose — re-deriving would call
-- GetOrCreateContainerCfg, which can CREATE a container if the crew's was
-- stopped in between, and a hard stop must never have the side effect of
-- starting a new container. Recording the container the run actually used
-- keeps hard-stop read-only against container lifecycle.
--
-- hard_stop_at / hard_stop_result: what a Tier 2 attempt actually did,
-- independent of exec_id/exec_container_id which describe the run, not the
-- attempt. hard_stop_result is a short fixed vocabulary (see the CHECK) —
-- process identifiers and a routing-style outcome, exactly the class of
-- diagnostic column lease_owner already established as "not user text"
-- (20260904201648_assignments_lease.sql); no data_subject_id follows for
-- the same reason.
--
-- All four nullable, no default: NULL means either "predates this
-- migration" or "this run's dispatch path never reached an exec" (an early
-- provisioning/build failure finishes an assignment without ever calling
-- Exec) — never backfilled, since there is no historical ExecResult left to
-- recover one from.
ALTER TABLE assignments ADD COLUMN exec_id TEXT;
ALTER TABLE assignments ADD COLUMN exec_container_id TEXT;
ALTER TABLE assignments ADD COLUMN hard_stop_at TEXT;
ALTER TABLE assignments ADD COLUMN hard_stop_result TEXT
    CHECK (hard_stop_result IS NULL OR hard_stop_result IN (
        'TERMINATED_TERM', 'TERMINATED_KILL', 'ALREADY_EXITED',
        'UNSUPPORTED', 'NOT_FOUND', 'ERROR', 'PENDING_EXEC'
    ));
