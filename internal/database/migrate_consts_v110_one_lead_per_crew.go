package database

// migrationOneLeadPerCrew (v110) enforces the "at most one LEAD per crew"
// invariant at the database level via a partial unique index.
//
// Why: the create + promote paths in internal/api/agents_create.go and
// agents_update.go used a check-then-act sequence ("SELECT ... WHERE
// crew_id=? AND agent_role='LEAD'", then INSERT/UPDATE) with no DB
// constraint backing it. Two concurrent requests could both pass the
// SELECT (find no lead) and then both write a LEAD into the same crew —
// a classic TOCTOU race yielding two leads, which BuildLeadContext and
// the orchestrator assume can never happen.
//
// A partial unique index makes the invariant atomic and code-path
// independent: any second LEAD insert/promotion for a crew fails with a
// UNIQUE constraint violation, which the handlers translate to 409. The
// index is partial so it only constrains live LEAD rows — AGENT rows and
// soft-deleted (deleted_at IS NOT NULL) leads are exempt, so demoting /
// re-creating a lead works normally.
//
// Remediation-first: any install that hit the old TOCTOU race may already
// hold two live LEADs in one crew, which would make CREATE UNIQUE INDEX
// fail and abort the whole migration. So before creating the index we
// demote the extras — keeping the earliest (lowest rowid) LEAD per crew
// and flipping the rest to AGENT. Crew-less LEADs (crew_id IS NULL) are
// left alone: the partial index is on crew_id, and SQLite treats NULLs as
// distinct, so they never collide. The remediation + index run in one
// transaction (see migrate.go), so they apply atomically. IF NOT EXISTS
// keeps the index creation idempotent.
const migrationOneLeadPerCrew = `
UPDATE agents SET agent_role = 'AGENT'
WHERE agent_role = 'LEAD' AND deleted_at IS NULL AND crew_id IS NOT NULL
  AND rowid NOT IN (
    SELECT MIN(rowid) FROM agents
    WHERE agent_role = 'LEAD' AND deleted_at IS NULL AND crew_id IS NOT NULL
    GROUP BY crew_id
  );

CREATE UNIQUE INDEX IF NOT EXISTS idx_agents_one_lead_per_crew
    ON agents(crew_id)
    WHERE agent_role = 'LEAD' AND deleted_at IS NULL;
`
